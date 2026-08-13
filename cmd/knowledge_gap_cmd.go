package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"time"

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/evidence"
	"github.com/forge/forge/internal/knowledgegap"
	"github.com/forge/forge/internal/observations"
	"github.com/forge/forge/internal/skills"
	"github.com/forge/forge/internal/streams"
)

// runKnowledgeGap surfaces recurring technical knowledge gaps and vocabulary
// mismatches from local forge signals — one LLM call over locally-derived
// evidence, same idiom as runProfile.
func runKnowledgeGap(args []string) {
	fs := flag.NewFlagSet("knowledge-gap", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	all := fs.Bool("all", false, "run across all known projects")
	vocab := fs.Bool("vocab", false, "vocabulary-only mode (skip gap section)")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	coachModeFlag := fs.String("coach-mode", "quiet", "queue gaps as coaching items (off/observe/quiet/normal/strict)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	coachMode, err := coach.ParseMode(*coachModeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if *all {
		projects, err := knownProjects(database)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(projects) == 0 {
			fmt.Println("No known projects found.")
			return
		}
		for _, p := range projects {
			result, hasSessions, err := knowledgeGapOne(database, p.ID, *vocab)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", p.ID, err)
				continue
			}
			if !hasSessions {
				fmt.Printf("%s: no sessions found.\n\n", p.ID)
				continue
			}
			printKnowledgeGap(p.ID, result, *vocab, *jsonOut)
			if queued, err := queueKnowledgeGaps(database, p.ID, result, coachMode); err != nil {
				fmt.Fprintf(os.Stderr, "  (coach queueing failed: %v)\n", err)
			} else if queued > 0 {
				fmt.Printf("  queued %d coaching item(s)\n", queued)
			}
			fmt.Println()
		}
		return
	}

	repoPath := *path
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	gitRoot := repoPath
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel").Output(); err == nil {
		gitRoot = strings.TrimSpace(string(out))
	}
	projectID := detectProjectIDForPath(gitRoot)

	result, hasSessions, err := knowledgeGapOne(database, projectID, *vocab)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !hasSessions {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}
	printKnowledgeGap(projectID, result, *vocab, *jsonOut)
	if queued, err := queueKnowledgeGaps(database, projectID, result, coachMode); err != nil {
		fmt.Fprintf(os.Stderr, "Coach queueing failed: %v\n", err)
	} else if queued > 0 {
		fmt.Printf("  queued %d coaching item(s)\n", queued)
	}
}

const maxKGPromptSamples = 20

// queueKnowledgeGaps records each detected gap as an auditable Observation,
// evolves its project-local skill state through the same deterministic
// evaluator the verification detector uses, then queues any that just
// crossed into suspected_gap as coaching items. A gap only starts coaching
// once it has recurred across separate knowledge-gap runs — skills.Evaluate
// requires two failed-application observations before suspected_gap, so a
// one-off LLM-flagged gap is recorded as evidence but not yet surfaced.
func queueKnowledgeGaps(database *db.DB, projectID string, result knowledgegap.Result, mode coach.Mode) (int, error) {
	if len(result.Gaps) == 0 || mode == coach.ModeOff {
		return 0, nil
	}

	store := evidence.Store{DB: database, ExtractorVersion: "1"}
	observedAt := time.Now().UTC().Format(time.RFC3339)

	for _, gap := range result.Gaps {
		skillKey := knowledgegap.GapSkillKey(gap.Topic)
		draft := observations.ObservationDraft{
			Kind:       "knowledge_gap",
			SkillKey:   skillKey,
			Confidence: knowledgegap.ObservationConfidence,
			Severity:   "medium",
			Summary:    gap.ConcreteLesson,
			SupportingSources: []observations.SourceReference{{
				SourceType: "knowledge_gap_evidence",
				SourceID:   skillKey,
				Excerpt:    gap.Evidence,
			}},
		}
		if _, err := store.Save(projectID, draft, true); err != nil {
			return 0, fmt.Errorf("save observation for %q: %w", gap.Topic, err)
		}

		state, err := database.GetSkillState(skillKey, skills.ScopeProject, projectID)
		if err != nil {
			return 0, fmt.Errorf("get skill state for %q: %w", skillKey, err)
		}
		if state == nil {
			state = &db.SkillState{SkillKey: skillKey, ScopeType: skills.ScopeProject, ScopeID: projectID}
		}
		next, _ := skills.Evaluate(*state, skills.EvidenceSummary{
			Confidence:         knowledgegap.ObservationConfidence,
			FailedApplications: 1,
			ObservedAt:         observedAt,
		})
		if err := database.UpsertSkillState(&next); err != nil {
			return 0, fmt.Errorf("update skill state for %q: %w", skillKey, err)
		}
	}

	return coach.QueueEligible(database, projectID, mode)
}

// knowledgeGapOne gathers Signals for a project and runs the LLM pass.
func knowledgeGapOne(database *db.DB, projectID string, vocabOnly bool) (knowledgegap.Result, bool, error) {
	sig := knowledgegap.Signals{ProjectID: projectID}

	patterns, err := database.RecentCrossSessionPatternsByProject(projectID, 50)
	if err != nil {
		return knowledgegap.Result{}, false, err
	}
	for _, p := range patterns {
		switch p.PatternType {
		case "knowledge_gap":
			sig.KnowledgeGapPatterns = append(sig.KnowledgeGapPatterns, p.Pattern)
		case "recurring_failure":
			sig.RecurringFailures = append(sig.RecurringFailures, p.Pattern)
		}
	}

	principles, err := database.RecentPrinciplesByProject(projectID, 50)
	if err != nil {
		return knowledgegap.Result{}, false, err
	}
	for _, p := range principles {
		text := p.Title
		if p.Narrative != "" {
			text += ": " + p.Narrative
		}
		switch p.Type {
		case "gotcha":
			sig.GotchaPrinciples = append(sig.GotchaPrinciples, text)
		case "bugfix":
			sig.BugfixPrinciples = append(sig.BugfixPrinciples, text)
		}
	}

	failureSigs, err := database.FailureSignaturesByProject(projectID)
	if err != nil {
		return knowledgegap.Result{}, false, err
	}
	sig.FailureBuckets = knowledgegap.BucketFailures(failureSigs)

	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		return knowledgegap.Result{}, false, err
	}
	if len(ranges) == 0 {
		return knowledgegap.Result{}, false, nil
	}
	if _, err := streams.Cluster(ranges, streams.DefaultGap); err != nil {
		return knowledgegap.Result{}, false, err
	}

	var allPrompts []string
	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			return knowledgegap.Result{}, false, err
		}
		for _, e := range events {
			if e.EventType != "UserPromptSubmit" && e.EventType != "beforeSubmitPrompt" {
				continue
			}
			if text := distill.ExtractPromptText(e.Payload); text != "" {
				allPrompts = append(allPrompts, text)
			}
		}
	}
	sig.PromptSamples = knowledgegap.FilterDecisionPrompts(allPrompts)
	if len(sig.PromptSamples) > maxKGPromptSamples {
		sig.PromptSamples = sig.PromptSamples[len(sig.PromptSamples)-maxKGPromptSamples:]
	}
	sig.VocabTerms = knowledgegap.ExtractVocabTerms(sig.PromptSamples)

	prompt := knowledgegap.BuildPrompt(sig)
	if vocabOnly {
		sig.KnowledgeGapPatterns = nil
		sig.RecurringFailures = nil
		sig.GotchaPrinciples = nil
		sig.BugfixPrinciples = nil
		sig.FailureBuckets = nil
		prompt = knowledgegap.BuildVocabOnlyPrompt(sig)
	}

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)
	resp, err := d.CallLLM(prompt)
	if err != nil {
		return knowledgegap.Result{}, true, fmt.Errorf("calling LLM (%w). Configure a provider: forge config --provider <name> --api-key <key>", err)
	}

	result, err := knowledgegap.ParseResult(resp)
	if err != nil {
		return knowledgegap.Result{}, true, fmt.Errorf("parsing model response: %w\nRaw response:\n%s", err, resp)
	}
	return result, true, nil
}

func printKnowledgeGap(projectID string, result knowledgegap.Result, vocabOnly, jsonOut bool) {
	if jsonOut {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Printf("%s:\n%s\n", projectID, b)
		return
	}

	fmt.Printf("%s knowledge gaps\n\n", projectID)
	if !vocabOnly {
		if len(result.Gaps) == 0 {
			fmt.Println("  No recurring gaps found in local evidence.")
		}
		for i, g := range result.Gaps {
			fmt.Printf("  %d. %s\n     evidence: %s\n     lesson: %s\n     study: %s\n\n", i+1, g.Topic, g.Evidence, g.ConcreteLesson, g.StudyPointer)
		}
	}

	if len(result.Vocabulary) > 0 {
		fmt.Println("  Vocabulary corrections:")
		for _, v := range result.Vocabulary {
			fmt.Printf("  - %q -> %q: %s\n", v.TermUsed, v.CorrectTerm, v.Why)
		}
	} else if vocabOnly {
		fmt.Println("  No vocabulary mismatches found in local evidence.")
	}
}
