package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SkillDefinition struct {
	Key               string `json:"key"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Transferable      bool   `json:"transferable"`
	DefinitionVersion string `json:"definition_version"`
}

type Observation struct {
	ID               string  `json:"id"`
	CreatedAt        string  `json:"created_at"`
	ProjectID        string  `json:"project_id"`
	SessionID        string  `json:"session_id"`
	Kind             string  `json:"kind"`
	SkillKey         string  `json:"skill_key"`
	Confidence       float64 `json:"confidence"`
	Severity         string  `json:"severity"`
	Status           string  `json:"status"`
	Summary          string  `json:"summary"`
	ExtractorVersion string  `json:"extractor_version"`
}

type ObservationEvidence struct {
	ObservationID string `json:"observation_id"`
	SourceType    string `json:"source_type"`
	SourceID      string `json:"source_id"`
	Role          string `json:"role"`
	Excerpt       string `json:"excerpt"`
}

type SkillState struct {
	SkillKey               string  `json:"skill_key"`
	ScopeType              string  `json:"scope_type"`
	ScopeID                string  `json:"scope_id"`
	State                  string  `json:"state"`
	Confidence             float64 `json:"confidence"`
	EvidenceCount          int     `json:"evidence_count"`
	SuccessfulApplications int     `json:"successful_applications"`
	FailedApplications     int     `json:"failed_applications"`
	LastObservedAt         string  `json:"last_observed_at"`
	UpdatedAt              string  `json:"updated_at"`
}

type CoachingItem struct {
	ID            string `json:"id"`
	ObservationID string `json:"observation_id"`
	SkillKey      string `json:"skill_key"`
	ProjectID     string `json:"project_id"`
	Status        string `json:"status"`
	DeliveryMode  string `json:"delivery_mode"`
	Question      string `json:"question"`
	NextAction    string `json:"next_action"`
	Lesson        string `json:"lesson"`
	CreatedAt     string `json:"created_at"`
	SurfacedAt    string `json:"surfaced_at"`
	ResolvedAt    string `json:"resolved_at"`
	Resolution    string `json:"resolution"`
}

func nowIfEmpty(value *string) {
	if *value == "" {
		*value = time.Now().UTC().Format(time.RFC3339)
	}
}

func (d *DB) InsertObservation(observation *Observation) error {
	if observation.ID == "" {
		observation.ID = uuid.New().String()
	}
	nowIfEmpty(&observation.CreatedAt)
	if observation.ExtractorVersion == "" {
		observation.ExtractorVersion = "1"
	}
	_, err := d.conn.Exec(`INSERT OR IGNORE INTO observations
		(id, created_at, project_id, session_id, kind, skill_key, confidence, severity, status, summary, extractor_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, observation.ID, observation.CreatedAt, observation.ProjectID,
		observation.SessionID, observation.Kind, observation.SkillKey, observation.Confidence, observation.Severity,
		observation.Status, observation.Summary, observation.ExtractorVersion)
	return err
}

func (d *DB) AddObservationEvidence(evidence *ObservationEvidence) error {
	_, err := d.conn.Exec(`INSERT OR IGNORE INTO observation_evidence
		(observation_id, source_type, source_id, role, excerpt) VALUES (?, ?, ?, ?, ?)`, evidence.ObservationID,
		evidence.SourceType, evidence.SourceID, evidence.Role, evidence.Excerpt)
	return err
}

func (d *DB) ListObservationEvidence(observationID string) ([]ObservationEvidence, error) {
	rows, err := d.conn.Query(`SELECT observation_id, source_type, source_id, role, excerpt
		FROM observation_evidence WHERE observation_id=? ORDER BY source_type, source_id, role`, observationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ObservationEvidence
	for rows.Next() {
		var item ObservationEvidence
		if err := rows.Scan(&item.ObservationID, &item.SourceType, &item.SourceID, &item.Role, &item.Excerpt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ObservationByID returns one observation for CLI and service detail views.
func (d *DB) ObservationByID(id string) (Observation, bool, error) {
	var observation Observation
	err := d.conn.QueryRow(`SELECT id, created_at, project_id, session_id, kind, skill_key, confidence, severity, status, summary, extractor_version
		FROM observations WHERE id=?`, id).Scan(&observation.ID, &observation.CreatedAt, &observation.ProjectID, &observation.SessionID,
		&observation.Kind, &observation.SkillKey, &observation.Confidence, &observation.Severity, &observation.Status,
		&observation.Summary, &observation.ExtractorVersion)
	if err == sql.ErrNoRows {
		return Observation{}, false, nil
	}
	if err != nil {
		return Observation{}, false, err
	}
	return observation, true, nil
}

func (d *DB) ListObservations(projectID string, status string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = 100
	}
	return d.listObservations(projectID, status, limit)
}

// ListAllObservations returns every matching observation for internal
// reconciliation paths. User-facing queries should use ListObservations.
func (d *DB) ListAllObservations(projectID, status string) ([]Observation, error) {
	return d.listObservations(projectID, status, 0)
}

func (d *DB) listObservations(projectID, status string, limit int) ([]Observation, error) {
	query := `SELECT id, created_at, project_id, session_id, kind, skill_key, confidence, severity, status, summary, extractor_version
		FROM observations WHERE project_id=?`
	args := []any{projectID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Observation
	for rows.Next() {
		var item Observation
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.ProjectID, &item.SessionID, &item.Kind, &item.SkillKey,
			&item.Confidence, &item.Severity, &item.Status, &item.Summary, &item.ExtractorVersion); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (d *DB) GetSkillState(skillKey, scopeType, scopeID string) (*SkillState, error) {
	var state SkillState
	err := d.conn.QueryRow(`SELECT skill_key, scope_type, scope_id, state, confidence, evidence_count,
		successful_applications, failed_applications, last_observed_at, updated_at FROM skill_states
		WHERE skill_key=? AND scope_type=? AND scope_id=?`, skillKey, scopeType, scopeID).Scan(
		&state.SkillKey, &state.ScopeType, &state.ScopeID, &state.State, &state.Confidence, &state.EvidenceCount,
		&state.SuccessfulApplications, &state.FailedApplications, &state.LastObservedAt, &state.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (d *DB) UpsertSkillState(state *SkillState) error {
	return upsertSkillState(d.conn, state)
}

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertSkillState(executor sqlExecutor, state *SkillState) error {
	nowIfEmpty(&state.UpdatedAt)
	_, err := executor.Exec(`INSERT INTO skill_states
		(skill_key, scope_type, scope_id, state, confidence, evidence_count, successful_applications,
		 failed_applications, last_observed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(skill_key, scope_type, scope_id) DO UPDATE SET state=excluded.state, confidence=excluded.confidence,
		 evidence_count=excluded.evidence_count, successful_applications=excluded.successful_applications,
		 failed_applications=excluded.failed_applications, last_observed_at=excluded.last_observed_at,
		 updated_at=excluded.updated_at`, state.SkillKey, state.ScopeType, state.ScopeID, state.State, state.Confidence,
		state.EvidenceCount, state.SuccessfulApplications, state.FailedApplications, state.LastObservedAt, state.UpdatedAt)
	return err
}

// ApplySkillStateOnce persists a skill-state transition and its durable
// processing marker together. If marker persistence fails, the state update
// is rolled back so a retry can safely apply the observation once.
func (d *DB) ApplySkillStateOnce(observationID string, state *SkillState, marker ObservationEvidence) (bool, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRow(`SELECT 1 FROM observation_evidence WHERE observation_id=? AND source_type=? AND source_id=? AND role=?`,
		observationID, marker.SourceType, marker.SourceID, marker.Role).Scan(&exists)
	if err == nil {
		return false, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("find processing marker: %w", err)
	}
	if err := upsertSkillState(tx, state); err != nil {
		return false, fmt.Errorf("update skill state: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO observation_evidence
		(observation_id, source_type, source_id, role, excerpt) VALUES (?, ?, ?, ?, ?)`, observationID,
		marker.SourceType, marker.SourceID, marker.Role, marker.Excerpt); err != nil {
		return false, fmt.Errorf("mark observation processed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (d *DB) InsertCoachingItem(item *CoachingItem) error {
	if item.ID == "" {
		item.ID = uuid.New().String()
	}
	nowIfEmpty(&item.CreatedAt)
	_, err := d.conn.Exec(`INSERT OR IGNORE INTO coaching_items
		(id, observation_id, skill_key, project_id, status, delivery_mode, question, next_action, lesson,
		 created_at, surfaced_at, resolved_at, resolution) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID,
		item.ObservationID, item.SkillKey, item.ProjectID, item.Status, item.DeliveryMode, item.Question, item.NextAction,
		item.Lesson, item.CreatedAt, item.SurfacedAt, item.ResolvedAt, item.Resolution)
	return err
}

func (d *DB) ListCoachingItems(projectID, status string, limit int) ([]CoachingItem, error) {
	if limit <= 0 {
		limit = 100
	}
	return d.listCoachingItems(projectID, status, limit)
}

// ListAllCoachingItems returns every matching coaching item for internal
// lifecycle and suppression checks. User-facing queries should use
// ListCoachingItems.
func (d *DB) ListAllCoachingItems(projectID, status string) ([]CoachingItem, error) {
	return d.listCoachingItems(projectID, status, 0)
}

// CoachingItemByObservationID returns the coaching item attached to an
// observation, if it has been queued.
func (d *DB) CoachingItemByObservationID(observationID string) (CoachingItem, bool, error) {
	var item CoachingItem
	err := d.conn.QueryRow(`SELECT id, observation_id, skill_key, project_id, status, delivery_mode, question, next_action, lesson,
		created_at, surfaced_at, resolved_at, resolution FROM coaching_items WHERE observation_id=?`, observationID).Scan(
		&item.ID, &item.ObservationID, &item.SkillKey, &item.ProjectID, &item.Status, &item.DeliveryMode, &item.Question,
		&item.NextAction, &item.Lesson, &item.CreatedAt, &item.SurfacedAt, &item.ResolvedAt, &item.Resolution)
	if err == sql.ErrNoRows {
		return CoachingItem{}, false, nil
	}
	if err != nil {
		return CoachingItem{}, false, err
	}
	return item, true, nil
}

func (d *DB) listCoachingItems(projectID, status string, limit int) ([]CoachingItem, error) {
	query := `SELECT id, observation_id, skill_key, project_id, status, delivery_mode, question, next_action, lesson,
		created_at, surfaced_at, resolved_at, resolution FROM coaching_items WHERE project_id=?`
	args := []any{projectID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CoachingItem
	for rows.Next() {
		var item CoachingItem
		if err := rows.Scan(&item.ID, &item.ObservationID, &item.SkillKey, &item.ProjectID, &item.Status, &item.DeliveryMode,
			&item.Question, &item.NextAction, &item.Lesson, &item.CreatedAt, &item.SurfacedAt, &item.ResolvedAt, &item.Resolution); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (d *DB) UpdateCoachingItem(item *CoachingItem) error {
	_, err := d.conn.Exec(`UPDATE coaching_items SET status=?, surfaced_at=?, resolved_at=?, resolution=? WHERE id=?`,
		item.Status, item.SurfacedAt, item.ResolvedAt, item.Resolution, item.ID)
	return err
}
