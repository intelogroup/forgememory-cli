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
	if len(args) > 0 {
		switch args[0] {
		case "status":
			runCoachStatus(args[1:])
			return
		case "list":
			runCoachList(args[1:])
			return
		case "explain":
			runCoachExplain(args[1:])
			return
		case "accept":
			runCoachAction(args[1:], "accept")
			return
		case "defer":
			runCoachAction(args[1:], "defer")
			return
		case "dismiss":
			runCoachAction(args[1:], "dismiss")
			return
		case "review":
			runCoachReview(args[1:])
			return
		}
	}

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

type coachListPayload struct {
	ProjectID string          `json:"project_id"`
	Items     []coachListItem `json:"items"`
}

type coachListItem struct {
	ID                string  `json:"id"`
	ObservationID     string  `json:"observation_id"`
	SkillKey          string  `json:"skill_key"`
	Status            string  `json:"status"`
	Confidence        float64 `json:"confidence"`
	State             string  `json:"state"`
	EvidenceCount     int     `json:"evidence_count"`
	NeedsMoreEvidence bool    `json:"needs_more_evidence"`
}

func runCoachList(args []string) {
	fs := flag.NewFlagSet("coach list", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	projectID := coachProjectID(*path)
	items, err := database.ListCoachingItems(projectID, "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	payload := coachListPayload{ProjectID: projectID, Items: make([]coachListItem, 0, len(items))}
	for _, item := range items {
		entry, err := coachListEntryForItem(database, item)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		payload.Items = append(payload.Items, entry)
	}
	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(payload.Items) == 0 {
		fmt.Printf("%s: no coaching items.\n", projectID)
		return
	}
	for _, item := range payload.Items {
		fmt.Printf("[%s] status=%s skill=%s confidence=%.2f evidence=%d needs-more-evidence=%t\n",
			item.ID, item.Status, item.SkillKey, item.Confidence, item.EvidenceCount, item.NeedsMoreEvidence)
	}
}

func coachListEntryForItem(database *db.DB, item db.CoachingItem) (coachListItem, error) {
	entry := coachListItem{ID: item.ID, ObservationID: item.ObservationID, SkillKey: item.SkillKey, Status: item.Status, NeedsMoreEvidence: true}
	state, err := database.GetSkillState(item.SkillKey, skills.ScopeProject, item.ProjectID)
	if err != nil {
		return coachListItem{}, fmt.Errorf("get skill state: %w", err)
	}
	if state != nil {
		entry.Confidence = state.Confidence
		entry.State = state.State
		entry.EvidenceCount = state.EvidenceCount
		entry.NeedsMoreEvidence = state.State != skills.StateSuspectedGap
	}
	return entry, nil
}

type coachExplainPayload struct {
	ProjectID          string                   `json:"project_id"`
	Observation        db.Observation           `json:"observation"`
	Item               coachListItem            `json:"item"`
	SupportingEvidence []db.ObservationEvidence `json:"supporting_evidence"`
	CounterEvidence    []db.ObservationEvidence `json:"counter_evidence"`
	NeedsMoreEvidence  bool                     `json:"needs_more_evidence"`
}

func runCoachExplain(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		coachCommandUsage("forge coach explain <observation-id> [--json]")
	}
	observationID := args[0]
	fs := flag.NewFlagSet("coach explain", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		coachCommandUsage("forge coach explain <observation-id> [--json]")
	}
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()
	observation, found, err := database.ObservationByID(observationID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !found {
		fmt.Fprintf(os.Stderr, "Error: observation %q was not found\n", observationID)
		os.Exit(1)
	}
	item, found, err := database.CoachingItemByObservationID(observationID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !found {
		fmt.Fprintf(os.Stderr, "Error: observation %q has no coaching item\n", observationID)
		os.Exit(1)
	}
	entry, err := coachListEntryForItem(database, item)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	evidence, err := database.ListObservationEvidence(observationID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	payload := coachExplainPayload{ProjectID: observation.ProjectID, Observation: observation, Item: entry, SupportingEvidence: []db.ObservationEvidence{}, CounterEvidence: []db.ObservationEvidence{}, NeedsMoreEvidence: entry.NeedsMoreEvidence}
	for _, source := range evidence {
		switch source.Role {
		case "supporting":
			payload.SupportingEvidence = append(payload.SupportingEvidence, source)
		case "counter":
			payload.CounterEvidence = append(payload.CounterEvidence, source)
		}
	}
	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("[%s] status=%s skill=%s confidence=%.2f state=%s evidence=%d needs-more-evidence=%t\n%s\n",
		entry.ID, entry.Status, entry.SkillKey, entry.Confidence, entry.State, entry.EvidenceCount, entry.NeedsMoreEvidence, observation.Summary)
	printCoachEvidence("supporting evidence", payload.SupportingEvidence)
	printCoachEvidence("counter-evidence", payload.CounterEvidence)
}

func printCoachEvidence(label string, evidence []db.ObservationEvidence) {
	fmt.Printf("%s:\n", label)
	if len(evidence) == 0 {
		fmt.Println("  none")
		return
	}
	for _, source := range evidence {
		fmt.Printf("  - %s\n", source.Excerpt)
	}
}

func runCoachAction(args []string, action string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		coachCommandUsage("forge coach " + action + " <observation-id>" + coachActionUsageSuffix(action))
	}
	observationID := args[0]
	fs := flag.NewFlagSet("coach "+action, flag.ContinueOnError)
	reason := fs.String("reason", "", "dismissal category: not_relevant, already_known, incorrect, or never_show_again")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		coachCommandUsage("forge coach " + action + " <observation-id>" + coachActionUsageSuffix(action))
	}
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()
	switch action {
	case "accept":
		err = coach.AcceptObservation(database, observationID)
	case "defer":
		err = coach.DeferObservation(database, observationID)
	case "dismiss":
		err = coach.DismissObservation(database, observationID, *reason)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	verb := map[string]string{"accept": "Accepted", "defer": "Deferred", "dismiss": "Dismissed"}[action]
	fmt.Printf("%s.\n", verb)
}

func coachActionUsageSuffix(action string) string {
	if action == "dismiss" {
		return " --reason <category>"
	}
	return ""
}

func runCoachReview(args []string) {
	fs := flag.NewFlagSet("coach review", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()
	projectID := coachProjectID(*path)
	items, err := database.ListCoachingItems(projectID, "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	payload := coachListPayload{ProjectID: projectID, Items: make([]coachListItem, 0, len(items))}
	for _, item := range items {
		entry, err := coachListEntryForItem(database, item)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		payload.Items = append(payload.Items, entry)
	}
	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("%s coach review\n", projectID)
	for _, item := range payload.Items {
		fmt.Printf("[%s] status=%s confidence=%.2f needs-more-evidence=%t\n", item.ObservationID, item.Status, item.Confidence, item.NeedsMoreEvidence)
	}
}

func coachCommandUsage(usage string) {
	fmt.Fprintf(os.Stderr, "Usage: %s\n", usage)
	os.Exit(1)
}

type coachStatusPayload struct {
	ProjectID string               `json:"project_id"`
	Counts    coachLifecycleCounts `json:"counts"`
}

type coachLifecycleCounts struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Surfaced  int `json:"surfaced"`
	Accepted  int `json:"accepted"`
	Deferred  int `json:"deferred"`
	Dismissed int `json:"dismissed"`
}

func runCoachStatus(args []string) {
	fs := flag.NewFlagSet("coach status", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	projectID := coachProjectID(*path)
	items, err := database.ListCoachingItems(projectID, "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	payload := coachStatusPayload{ProjectID: projectID, Counts: countCoachItems(items)}
	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("%s coaching items: total=%d queued=%d surfaced=%d accepted=%d deferred=%d dismissed=%d\n",
		projectID, payload.Counts.Total, payload.Counts.Queued, payload.Counts.Surfaced,
		payload.Counts.Accepted, payload.Counts.Deferred, payload.Counts.Dismissed)
}

func coachProjectID(repoPath string) string {
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	gitRoot := repoPath
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel").Output(); err == nil {
		gitRoot = strings.TrimSpace(string(out))
	}
	return detectProjectIDForPath(gitRoot)
}

func countCoachItems(items []db.CoachingItem) coachLifecycleCounts {
	counts := coachLifecycleCounts{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case "queued":
			counts.Queued++
		case "surfaced":
			counts.Surfaced++
		case "accepted":
			counts.Accepted++
		case "deferred":
			counts.Deferred++
		case "dismissed":
			counts.Dismissed++
		}
	}
	return counts
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
