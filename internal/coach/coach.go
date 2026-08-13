// Package coach queues concise, evidence-backed verification coaching.
package coach

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/skills"
	"github.com/google/uuid"
)

const (
	defaultQuestion   = "What behavior should the test prove?"
	defaultNextAction = "Identify one invariant and add the narrowest relevant test."

	statusQueued    = "queued"
	statusSurfaced  = "surfaced"
	statusAccepted  = "accepted"
	statusDeferred  = "deferred"
	statusDismissed = "dismissed"

	neverShowAgain = "never_show_again"
)

// Mode controls whether the coach records only evidence, queues lessons, or
// permits safe-boundary delivery. The empty value is observation-only.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeObserve Mode = "observe"
	ModeQuiet   Mode = "quiet"
	ModeNormal  Mode = "normal"
	ModeStrict  Mode = "strict"
)

// ParseMode normalizes one of the supported coaching modes.
func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		return ModeObserve, nil
	}
	switch mode {
	case ModeOff, ModeObserve, ModeQuiet, ModeNormal, ModeStrict:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid coach mode %q", value)
	}
}

// QueueEligible creates one queued item per eligible observation. A lesson is
// eligible only after repeated, high-confidence project-local evidence has
// moved its skill into suspected_gap.
func QueueEligible(database *db.DB, projectID string, mode Mode) (int, error) {
	if database == nil {
		return 0, fmt.Errorf("coach database is required")
	}
	if projectID == "" {
		return 0, fmt.Errorf("project ID is required")
	}
	parsedMode, err := ParseMode(string(mode))
	if err != nil {
		return 0, err
	}
	mode = parsedMode
	if mode == ModeOff || mode == ModeObserve {
		return 0, nil
	}

	observations, err := database.ListAllObservations(projectID, "active")
	if err != nil {
		return 0, fmt.Errorf("list observations: %w", err)
	}
	items, err := database.ListAllCoachingItems(projectID, "")
	if err != nil {
		return 0, fmt.Errorf("list coaching items: %w", err)
	}
	existing := make(map[string]bool, len(items))
	suppressed := make(map[string]bool)
	for _, item := range items {
		existing[item.ObservationID] = true
		if item.Status == statusDismissed && item.Resolution == neverShowAgain {
			suppressed[item.SkillKey] = true
		}
	}

	queued := 0
	for _, observation := range observations {
		if existing[observation.ID] || suppressed[observation.SkillKey] {
			continue
		}
		state, err := database.GetSkillState(observation.SkillKey, skills.ScopeProject, projectID)
		if err != nil {
			return queued, fmt.Errorf("get skill state for %s: %w", observation.SkillKey, err)
		}
		if state == nil || state.State != skills.StateSuspectedGap {
			continue
		}
		item := lessonFor(observation, mode)
		if err := database.InsertCoachingItem(&item); err != nil {
			return queued, fmt.Errorf("insert coaching item: %w", err)
		}
		queued++
	}
	return queued, nil
}

func lessonFor(observation db.Observation, mode Mode) db.CoachingItem {
	return db.CoachingItem{
		ID:            uuid.NewSHA1(uuid.NameSpaceURL, []byte("coaching\x00"+observation.ID)).String(),
		ObservationID: observation.ID,
		SkillKey:      observation.SkillKey,
		ProjectID:     observation.ProjectID,
		Status:        statusQueued,
		DeliveryMode:  string(mode),
		Question:      defaultQuestion,
		NextAction:    defaultNextAction,
		Lesson:        observation.Summary,
	}
}

// SafeBoundarySuggestion returns one item that may be shown at a safe prompt
// or session boundary. Queued items take priority; when none remain, deferred
// items become eligible again in newest-first order. Selection does not mutate
// the selected item.
func SafeBoundarySuggestion(database *db.DB, projectID string) (*db.CoachingItem, error) {
	if database == nil {
		return nil, fmt.Errorf("coach database is required")
	}
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	for _, status := range []string{statusQueued, statusDeferred} {
		items, err := database.ListAllCoachingItems(projectID, status)
		if err != nil {
			return nil, fmt.Errorf("list %s coaching items: %w", status, err)
		}
		for _, item := range items {
			if item.DeliveryMode == string(ModeNormal) || item.DeliveryMode == string(ModeStrict) {
				return &item, nil
			}
		}
	}
	return nil, nil
}

// Accept resolves a coaching item and moves its project skill state into
// learning through the shared deterministic state evaluator.
func Accept(database *db.DB, id string) error {
	item, err := coachingItem(database, id)
	if err != nil {
		return err
	}
	if err := requireOpenItem(item); err != nil {
		return err
	}
	state, err := database.GetSkillState(item.SkillKey, skills.ScopeProject, item.ProjectID)
	if err != nil {
		return fmt.Errorf("get skill state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("project skill state for coaching item %q was not found", id)
	}
	next, _ := skills.Evaluate(*state, skills.EvidenceSummary{Confidence: state.Confidence, Accepted: true})
	if err := database.UpsertSkillState(&next); err != nil {
		return fmt.Errorf("update skill state: %w", err)
	}
	return resolve(database, item, statusAccepted, statusAccepted, true)
}

// Defer records that the developer postponed a queued lesson without treating
// it as a completed resolution.
func Defer(database *db.DB, id string) error {
	item, err := coachingItem(database, id)
	if err != nil {
		return err
	}
	if err := requireOpenItem(item); err != nil {
		return err
	}
	return resolve(database, item, statusDeferred, statusDeferred, false)
}

// Dismiss records the developer's reason. never_show_again suppresses future
// lessons for the same project-local skill pattern without deleting evidence.
func Dismiss(database *db.DB, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("dismissal reason is required")
	}
	item, err := coachingItem(database, id)
	if err != nil {
		return err
	}
	if err := requireOpenItem(item); err != nil {
		return err
	}
	return resolve(database, item, statusDismissed, strings.TrimSpace(reason), true)
}

func coachingItem(database *db.DB, id string) (db.CoachingItem, error) {
	if database == nil {
		return db.CoachingItem{}, fmt.Errorf("coach database is required")
	}
	if strings.TrimSpace(id) == "" {
		return db.CoachingItem{}, fmt.Errorf("coaching item ID is required")
	}
	var item db.CoachingItem
	err := database.Conn().QueryRow(`SELECT id, observation_id, skill_key, project_id, status, delivery_mode, question, next_action, lesson,
		created_at, surfaced_at, resolved_at, resolution FROM coaching_items WHERE id=?`, id).Scan(
		&item.ID, &item.ObservationID, &item.SkillKey, &item.ProjectID, &item.Status, &item.DeliveryMode, &item.Question,
		&item.NextAction, &item.Lesson, &item.CreatedAt, &item.SurfacedAt, &item.ResolvedAt, &item.Resolution)
	if err == sql.ErrNoRows {
		return db.CoachingItem{}, fmt.Errorf("coaching item %q was not found", id)
	}
	if err != nil {
		return db.CoachingItem{}, fmt.Errorf("get coaching item: %w", err)
	}
	return item, nil
}

func requireOpenItem(item db.CoachingItem) error {
	if item.Status != statusQueued && item.Status != statusSurfaced && item.Status != statusDeferred {
		return fmt.Errorf("coaching item %q is already %s", item.ID, item.Status)
	}
	return nil
}

func resolve(database *db.DB, item db.CoachingItem, status, resolution string, resolved bool) error {
	item.Status = status
	item.Resolution = resolution
	if resolved {
		item.ResolvedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := database.UpdateCoachingItem(&item); err != nil {
		return fmt.Errorf("update coaching item: %w", err)
	}
	return nil
}
