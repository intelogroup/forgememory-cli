package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/evidence"
	"github.com/forge/forge/internal/observations"
	"github.com/forge/forge/internal/skills"
	"github.com/forge/forge/internal/streams"
)

// runCoach surfaces and manages queued coaching items — the delivery side of
// the evidence -> skill state -> coach pipeline. By default it also runs the
// deterministic verification detector over the project's sessions first, so
// `forge coach` alone closes the loop from raw events to a coaching item
// without a separate detection step.
func runCoach(args []string) {
	fs := flag.NewFlagSet("coach", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	mode := fs.String("mode", "quiet", "coaching delivery mode for newly queued items (off/observe/quiet/normal/strict)")
	accept := fs.String("accept", "", "accept a coaching item by ID")
	deferID := fs.String("defer", "", "defer a coaching item by ID")
	dismiss := fs.String("dismiss", "", "dismiss a coaching item by ID (requires --reason)")
	reason := fs.String("reason", "", "reason for --dismiss")
	noDetect := fs.Bool("no-detect", false, "skip verification detection, only list/act on existing items")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	switch {
	case *accept != "":
		if err := coach.Accept(database, *accept); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Accepted.")
		return
	case *deferID != "":
		if err := coach.Defer(database, *deferID); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Deferred.")
		return
	case *dismiss != "":
		if err := coach.Dismiss(database, *dismiss, *reason); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Dismissed.")
		return
	}

	coachMode, err := coach.ParseMode(*mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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

	if !*noDetect {
		queued, err := detectAndQueueVerification(database, projectID, coachMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Verification detection failed: %v\n", err)
		} else if queued > 0 {
			fmt.Printf("Queued %d new coaching item(s) from verification detection.\n\n", queued)
		}
	}

	items, err := database.ListCoachingItems(projectID, "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(items) == 0 {
		fmt.Printf("%s: no coaching items.\n", projectID)
		return
	}
	fmt.Printf("%s coaching items\n\n", projectID)
	for _, it := range items {
		fmt.Printf("  [%s] status=%s skill=%s\n    %s\n    next: %s\n\n", it.ID, it.Status, it.SkillKey, it.Lesson, it.NextAction)
	}
}

// detectAndQueueVerification runs the deterministic verification detector
// over each of the project's sessions, persists any new observations as
// evidence, evolves skill state through the same evaluator knowledge-gap
// queueing uses, and queues eligible items.
func detectAndQueueVerification(database *db.DB, projectID string, mode coach.Mode) (int, error) {
	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		return 0, fmt.Errorf("session ranges: %w", err)
	}
	if len(ranges) == 0 {
		return 0, nil
	}
	if _, err := streams.Cluster(ranges, streams.DefaultGap); err != nil {
		return 0, err
	}

	failures, err := database.FailureSignaturesByProject(projectID)
	if err != nil {
		return 0, fmt.Errorf("failure signatures: %w", err)
	}
	failuresBySession := map[string][]db.FailureSignature{}
	for _, f := range failures {
		failuresBySession[f.SessionID] = append(failuresBySession[f.SessionID], f)
	}

	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			return 0, fmt.Errorf("session events for %s: %w", r.SessionID, err)
		}
		commits, err := database.CommitsForSession(r.SessionID)
		if err != nil {
			return 0, fmt.Errorf("session commits for %s: %w", r.SessionID, err)
		}

		drafts := observations.DetectVerification(observations.Input{
			ProjectID: projectID,
			SessionID: r.SessionID,
			Events:    events,
			Commits:   commits,
			Failures:  failuresBySession[r.SessionID],
			Live:      false,
		})
		if len(drafts) == 0 {
			continue
		}

		store := evidence.Store{DB: database, SessionID: r.SessionID, ExtractorVersion: "1"}
		observedAt := time.Now().UTC().Format(time.RFC3339)
		for _, draft := range drafts {
			if _, err := store.Save(projectID, draft, false); err != nil {
				return 0, fmt.Errorf("save observation: %w", err)
			}

			state, err := database.GetSkillState(draft.SkillKey, skills.ScopeProject, projectID)
			if err != nil {
				return 0, fmt.Errorf("get skill state for %s: %w", draft.SkillKey, err)
			}
			if state == nil {
				state = &db.SkillState{SkillKey: draft.SkillKey, ScopeType: skills.ScopeProject, ScopeID: projectID}
			}
			next, _ := skills.Evaluate(*state, verificationEvidenceSummary(draft, observedAt))
			if err := database.UpsertSkillState(&next); err != nil {
				return 0, fmt.Errorf("update skill state for %s: %w", draft.SkillKey, err)
			}
		}
	}

	return coach.QueueEligible(database, projectID, mode)
}

// verificationEvidenceSummary maps a verification ObservationDraft onto the
// deterministic skill-state evaluator: "verification_detected" is a
// successful application of the verification skill (tests ran, relevant,
// before shipping); every other kind the detector emits
// (code_change_without_relevant_test, unresolved_failure_after_change,
// repeated_regression) represents a failed application.
func verificationEvidenceSummary(draft observations.ObservationDraft, observedAt string) skills.EvidenceSummary {
	summary := skills.EvidenceSummary{Confidence: draft.Confidence, ObservedAt: observedAt}
	if draft.Kind == "verification_detected" {
		summary.SuccessfulApplications = 1
	} else {
		summary.FailedApplications = 1
	}
	return summary
}
