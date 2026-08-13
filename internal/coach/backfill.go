package coach

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/evidence"
	"github.com/forge/forge/internal/observations"
	"github.com/forge/forge/internal/skills"
)

const (
	defaultBackfillLimit = 20
	maximumBackfillLimit = 100

	processingSourceType = "processing"
	processingSourceID   = "verification-state-v1"
	processingRole       = "applied"
)

// BackfillReport describes the bounded deterministic work completed by a
// historical pass. SessionsSkipped includes incomplete, mixed-project, and
// otherwise ambiguous sessions for which the detector emitted no claim.
type BackfillReport struct {
	SessionsSelected    int
	SessionsProcessed   int
	SessionsSkipped     int
	ObservationsCreated int
	CoachingQueued      int
}

type sessionCandidate struct {
	id string
	ts string
}

// Backfill replays only a bounded, high-signal subset of a project's history:
// sessions linked to commits and sessions with repeated failures. It uses the
// normal deterministic persistence pipeline with historical provenance and
// never makes inference-provider calls.
func Backfill(ctx context.Context, database *db.DB, projectID string, limit int) (BackfillReport, error) {
	if err := requireBackfillInputs(ctx, database, projectID); err != nil {
		return BackfillReport{}, err
	}
	limit = boundedBackfillLimit(limit)

	commits, err := database.SessionCommitsByProject(projectID, limit)
	if err != nil {
		return BackfillReport{}, fmt.Errorf("list session commits: %w", err)
	}
	failures, err := database.FailureSignaturesByProjectLimited(projectID, limit)
	if err != nil {
		return BackfillReport{}, fmt.Errorf("list failure signatures: %w", err)
	}

	candidates := highSignalSessions(commits, failures, limit)
	report := BackfillReport{SessionsSelected: len(candidates)}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		created, processed, err := processSession(ctx, database, projectID, candidate.id, failures, false)
		if err != nil {
			return report, fmt.Errorf("process historical session %s: %w", candidate.id, err)
		}
		if processed {
			report.SessionsProcessed++
		} else {
			report.SessionsSkipped++
		}
		report.ObservationsCreated += created
	}

	queued, err := QueueEligible(database, projectID, configuredMode())
	if err != nil {
		return report, fmt.Errorf("queue eligible coaching: %w", err)
	}
	report.CoachingQueued = queued
	return report, nil
}

// ProcessCompletedSession runs live deterministic coaching processing for a
// completed session. Observation and state persistence are retry-safe, so a
// daemon retry after a transient failure does not duplicate evidence or counts.
func ProcessCompletedSession(ctx context.Context, database *db.DB, sessionID string) error {
	if database == nil {
		return fmt.Errorf("coach database is required")
	}
	if strings.TrimSpace(sessionID) == "" || sessionID == "unknown" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	events, err := database.SessionEventsUpTo(sessionID, "", 0)
	if err != nil {
		return fmt.Errorf("session events: %w", err)
	}
	projectID, complete := completedSingleProject(events)
	if !complete {
		return nil
	}
	failures, err := database.FailureSignaturesByProject(projectID)
	if err != nil {
		return fmt.Errorf("failure signatures: %w", err)
	}
	if _, _, err := processSession(ctx, database, projectID, sessionID, failures, true); err != nil {
		return err
	}
	if _, err := QueueEligible(database, projectID, configuredMode()); err != nil {
		return fmt.Errorf("queue eligible coaching: %w", err)
	}
	return nil
}

func requireBackfillInputs(ctx context.Context, database *db.DB, projectID string) error {
	if database == nil {
		return fmt.Errorf("coach database is required")
	}
	if strings.TrimSpace(projectID) == "" {
		return fmt.Errorf("project ID is required")
	}
	return ctx.Err()
}

func boundedBackfillLimit(limit int) int {
	if limit <= 0 {
		return defaultBackfillLimit
	}
	if limit > maximumBackfillLimit {
		return maximumBackfillLimit
	}
	return limit
}

func highSignalSessions(commits []db.SessionCommitSummary, failures []db.FailureSignature, limit int) []sessionCandidate {
	bySession := make(map[string]sessionCandidate, len(commits)+len(failures))
	add := func(sessionID, ts string) {
		if sessionID == "" || sessionID == "unknown" {
			return
		}
		if existing, ok := bySession[sessionID]; !ok || ts > existing.ts {
			bySession[sessionID] = sessionCandidate{id: sessionID, ts: ts}
		}
	}
	for _, commit := range commits {
		add(commit.SessionID, commit.LastTS)
	}
	for _, failure := range failures {
		if failure.RepeatCount >= 2 {
			add(failure.SessionID, failure.LastSeenTS)
		}
	}

	candidates := make([]sessionCandidate, 0, len(bySession))
	for _, candidate := range bySession {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ts == candidates[j].ts {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].ts > candidates[j].ts
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func processSession(ctx context.Context, database *db.DB, projectID, sessionID string, allFailures []db.FailureSignature, live bool) (created int, processed bool, err error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	events, err := database.SessionEventsUpTo(sessionID, "", 0)
	if err != nil {
		return 0, false, fmt.Errorf("session events: %w", err)
	}
	actualProject, complete := completedSingleProject(events)
	if !complete || actualProject != projectID {
		return 0, false, nil
	}
	commits, err := database.CommitsForSession(sessionID)
	if err != nil {
		return 0, false, fmt.Errorf("session commits: %w", err)
	}
	failures := failuresForSession(allFailures, sessionID)
	drafts := observations.DetectVerification(observations.Input{
		ProjectID: projectID,
		SessionID: sessionID,
		Events:    events,
		Commits:   commits,
		Failures:  failures,
		Live:      live,
	})
	if len(drafts) == 0 {
		return 0, false, nil
	}

	store := evidence.Store{DB: database, SessionID: sessionID, ExtractorVersion: "1"}
	observedAt := time.Now().UTC().Format(time.RFC3339)
	for _, draft := range drafts {
		if err := ctx.Err(); err != nil {
			return created, true, err
		}
		known, err := observationExists(database, projectID, sessionID, draft)
		if err != nil {
			return created, true, err
		}
		observation, err := store.Save(projectID, draft, live)
		if err != nil {
			return created, true, fmt.Errorf("save observation: %w", err)
		}
		if !known {
			created++
		}
		if err := applyObservationState(database, observation.ID, projectID, draft, observedAt); err != nil {
			return created, true, err
		}
	}
	return created, true, nil
}

func completedSingleProject(events []db.Event) (string, bool) {
	if len(events) == 0 {
		return "", false
	}
	projectID := ""
	completed := false
	for _, event := range events {
		if event.ProjectID == "" || event.ProjectID == "unknown" {
			return "", false
		}
		if projectID == "" {
			projectID = event.ProjectID
		} else if projectID != event.ProjectID {
			return "", false
		}
		switch event.EventType {
		case "Stop", "SessionEnd", "AfterAgent":
			completed = true
		}
	}
	return projectID, completed
}

func failuresForSession(all []db.FailureSignature, sessionID string) []db.FailureSignature {
	var failures []db.FailureSignature
	for _, failure := range all {
		if failure.SessionID == sessionID {
			failures = append(failures, failure)
		}
	}
	return failures
}

func observationExists(database *db.DB, projectID, sessionID string, draft observations.ObservationDraft) (bool, error) {
	rows, err := database.Conn().Query(`SELECT id FROM observations WHERE project_id=? AND session_id=? AND kind=? AND skill_key=?`, projectID, sessionID, draft.Kind, draft.SkillKey)
	if err != nil {
		return false, fmt.Errorf("find matching observation: %w", err)
	}
	var observationIDs []string
	for rows.Next() {
		var observationID string
		if err := rows.Scan(&observationID); err != nil {
			rows.Close()
			return false, err
		}
		observationIDs = append(observationIDs, observationID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	expected := draftSourceKeys(draft)
	for _, observationID := range observationIDs {
		evidence, err := database.ListObservationEvidence(observationID)
		if err != nil {
			return false, err
		}
		actual := make(map[string]bool)
		for _, source := range evidence {
			if source.Role == "supporting" || source.Role == "counter" {
				actual[source.Role+"\x00"+source.SourceType+"\x00"+source.SourceID] = true
			}
		}
		if len(actual) == len(expected) {
			matches := true
			for key := range expected {
				if !actual[key] {
					matches = false
					break
				}
			}
			if matches {
				return true, nil
			}
		}
	}
	return false, nil
}

func draftSourceKeys(draft observations.ObservationDraft) map[string]bool {
	keys := make(map[string]bool, len(draft.SupportingSources)+len(draft.CounterSources))
	for _, source := range draft.SupportingSources {
		keys["supporting\x00"+source.SourceType+"\x00"+source.SourceID] = true
	}
	for _, source := range draft.CounterSources {
		keys["counter\x00"+source.SourceType+"\x00"+source.SourceID] = true
	}
	return keys
}

func applyObservationState(database *db.DB, observationID, projectID string, draft observations.ObservationDraft, observedAt string) error {
	state, err := database.GetSkillState(draft.SkillKey, skills.ScopeProject, projectID)
	if err != nil {
		return fmt.Errorf("get skill state for %s: %w", draft.SkillKey, err)
	}
	if state == nil {
		state = &db.SkillState{SkillKey: draft.SkillKey, ScopeType: skills.ScopeProject, ScopeID: projectID}
	}
	next, _ := skills.Evaluate(*state, verificationEvidenceSummary(draft, observedAt))
	_, err = database.ApplySkillStateOnce(observationID, &next, db.ObservationEvidence{
		ObservationID: observationID,
		SourceType:    processingSourceType,
		SourceID:      processingSourceID,
		Role:          processingRole,
	})
	if err != nil {
		return fmt.Errorf("apply skill state for %s: %w", draft.SkillKey, err)
	}
	return nil
}

func verificationEvidenceSummary(draft observations.ObservationDraft, observedAt string) skills.EvidenceSummary {
	summary := skills.EvidenceSummary{Confidence: draft.Confidence, ObservedAt: observedAt}
	if draft.Kind == "verification_detected" {
		summary.SuccessfulApplications = 1
	} else {
		summary.FailedApplications = 1
	}
	return summary
}

func configuredMode() Mode {
	mode, err := ParseMode(os.Getenv("FORGE_COACH_MODE"))
	if err != nil {
		return ModeObserve
	}
	return mode
}
