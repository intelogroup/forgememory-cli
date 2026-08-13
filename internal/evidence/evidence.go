// Package evidence persists detector output without retaining raw event data.
package evidence

import (
	"fmt"
	"sort"
	"strings"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/observations"
	"github.com/google/uuid"
)

const defaultExtractorVersion = "1"

// Store saves auditable observation summaries and their compact source refs.
// SessionID identifies the session that produced every draft saved by the store.
type Store struct {
	DB               *db.DB
	SessionID        string
	ExtractorVersion string
}

// Save records one observation. Its stable ID makes retries idempotent for a
// project, session, kind, and set of source identities, regardless of order.
func (s Store) Save(projectID string, draft observations.ObservationDraft, live bool) (db.Observation, error) {
	if s.DB == nil {
		return db.Observation{}, fmt.Errorf("evidence store database is required")
	}
	if projectID == "" || draft.Kind == "" || draft.SkillKey == "" {
		return db.Observation{}, fmt.Errorf("project ID, observation kind, and skill key are required")
	}

	version := s.ExtractorVersion
	if version == "" {
		version = defaultExtractorVersion
	}
	observation := db.Observation{
		ID:               observationID(projectID, s.SessionID, draft),
		ProjectID:        projectID,
		SessionID:        s.SessionID,
		Kind:             draft.Kind,
		SkillKey:         draft.SkillKey,
		Confidence:       draft.Confidence,
		Severity:         draft.Severity,
		Status:           "active",
		Summary:          draft.Summary,
		ExtractorVersion: version,
	}
	if err := s.DB.InsertObservation(&observation); err != nil {
		return db.Observation{}, fmt.Errorf("insert observation: %w", err)
	}
	for _, source := range sources(draft, "supporting", "counter") {
		if err := s.DB.AddObservationEvidence(&db.ObservationEvidence{
			ObservationID: observation.ID,
			SourceType:    source.SourceType,
			SourceID:      source.SourceID,
			Role:          source.Role,
			Excerpt:       source.Excerpt,
		}); err != nil {
			return db.Observation{}, fmt.Errorf("add observation evidence: %w", err)
		}
	}
	provenance := "backfill"
	if live {
		provenance = "live"
	}
	if err := s.DB.AddObservationEvidence(&db.ObservationEvidence{
		ObservationID: observation.ID,
		SourceType:    "ingestion",
		SourceID:      provenance,
		Role:          "provenance",
		Excerpt:       "extractor=" + version,
	}); err != nil {
		return db.Observation{}, fmt.Errorf("add provenance: %w", err)
	}
	return observation, nil
}

type sourceRecord struct {
	observations.SourceReference
	Role string
}

func sources(draft observations.ObservationDraft, supportingRole, counterRole string) []sourceRecord {
	result := make([]sourceRecord, 0, len(draft.SupportingSources)+len(draft.CounterSources))
	for _, source := range draft.SupportingSources {
		result = append(result, sourceRecord{SourceReference: source, Role: supportingRole})
	}
	for _, source := range draft.CounterSources {
		result = append(result, sourceRecord{SourceReference: source, Role: counterRole})
	}
	return result
}

func observationID(projectID, sessionID string, draft observations.ObservationDraft) string {
	identities := make([]string, 0, len(draft.SupportingSources)+len(draft.CounterSources))
	for _, source := range sources(draft, "supporting", "counter") {
		identities = append(identities, strings.Join([]string{source.Role, source.SourceType, source.SourceID}, "\x00"))
	}
	sort.Strings(identities)
	key := strings.Join(append([]string{projectID, sessionID, draft.Kind}, identities...), "\x00")
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(key)).String()
}
