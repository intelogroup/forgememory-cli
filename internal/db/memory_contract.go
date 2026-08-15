package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/forge/forge/internal/security"
	"github.com/google/uuid"
)

type MemoryKind string

const (
	MemoryEvidence  MemoryKind = "evidence"
	MemoryAssertion MemoryKind = "assertion"
	MemoryBelief    MemoryKind = "belief"
	MemoryResource  MemoryKind = "resource"
)

type MemoryStatus string

const (
	MemoryActive     MemoryStatus = "active"
	MemorySuperseded MemoryStatus = "superseded"
	MemoryDisputed   MemoryStatus = "disputed"
	MemoryRetracted  MemoryStatus = "retracted"
	MemoryDeleted    MemoryStatus = "deleted"
	MemoryExpired    MemoryStatus = "expired"
)

// SPIFEnvelope is ForgeMemo's storage contract for an opaque SPIF artifact.
// SPIF owns binary encoding and signature verification; ForgeMemo owns scope.
type SPIFEnvelope struct {
	SchemaVersion     string `json:"schema_version"`
	ArtifactID        string `json:"artifact_id"`
	ContentDigest     string `json:"content_digest"`
	SignerID          string `json:"signer_id"`
	CreatedAt         string `json:"created_at"`
	BoundaryID        string `json:"boundary_id"`
	PredecessorDigest string `json:"predecessor_digest,omitempty"`
	Canonicalization  string `json:"canonicalization"`
	EnvelopeBytes     []byte `json:"envelope_bytes"`
}

type MemoryRecord struct {
	ID                   string       `json:"id"`
	OwnerID              string       `json:"owner_id"`
	BoundaryID           string       `json:"boundary_id"`
	Kind                 MemoryKind   `json:"kind"`
	RevisionID           string       `json:"revision_id"`
	Status               MemoryStatus `json:"status"`
	Content              []byte       `json:"content"`
	ContentDigest        string       `json:"content_digest"`
	SourceActor          string       `json:"source_actor"`
	SourceType           string       `json:"source_type"`
	SourceRef            string       `json:"source_ref,omitempty"`
	CapturedAt           string       `json:"captured_at"`
	ObservedAt           string       `json:"observed_at,omitempty"`
	EffectiveFrom        string       `json:"effective_from,omitempty"`
	EffectiveUntil       string       `json:"effective_until,omitempty"`
	ParentRevisionIDs    []string     `json:"parent_revision_ids,omitempty"`
	SupportingIDs        []string     `json:"supporting_ids,omitempty"`
	ContradictingIDs     []string     `json:"contradicting_ids,omitempty"`
	DerivationID         string       `json:"derivation_id,omitempty"`
	Confidence           float64      `json:"confidence"`
	Freshness            float64      `json:"freshness"`
	SourceReliability    float64      `json:"source_reliability"`
	Ambiguity            float64      `json:"ambiguity"`
	Disagreement         float64      `json:"disagreement"`
	Sensitivity          string       `json:"sensitivity"`
	CreatedAt            string       `json:"created_at"`
	SupersedesRevisionID string       `json:"supersedes_revision_id,omitempty"`
	ArtifactID           string       `json:"artifact_id,omitempty"`
	SPIFEnvelope         []byte       `json:"spif_envelope"`
}

type CorrectionEvent struct {
	CorrectionID          string   `json:"correction_id"`
	OwnerID               string   `json:"owner_id"`
	BoundaryID            string   `json:"boundary_id"`
	TargetRevisionID      string   `json:"target_revision_id"`
	ReplacementRevisionID string   `json:"replacement_revision_id,omitempty"`
	Action                string   `json:"action"`
	ReasonCode            string   `json:"reason_code"`
	Explanation           string   `json:"explanation,omitempty"`
	ActorID               string   `json:"actor_id"`
	CreatedAt             string   `json:"created_at"`
	EvidenceIDs           []string `json:"evidence_ids,omitempty"`
	SPIFEnvelope          []byte   `json:"spif_envelope"`
}

type RetrievalEvent struct {
	RetrievalID           string   `json:"retrieval_id"`
	OwnerID               string   `json:"owner_id"`
	BoundaryID            string   `json:"boundary_id"`
	QueryDigest           string   `json:"query_digest"`
	CandidateRevisionIDs  []string `json:"candidate_revision_ids,omitempty"`
	SelectedRevisionIDs   []string `json:"selected_revision_ids,omitempty"`
	RankingVersion        string   `json:"ranking_version"`
	FeatureSnapshotDigest string   `json:"feature_snapshot_digest,omitempty"`
	CreatedAt             string   `json:"created_at"`
	Outcome               string   `json:"outcome"`
	OutcomeAt             string   `json:"outcome_at,omitempty"`
}

func memoryDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func memoryAAD(ownerID, boundaryID, revisionID, digest string) string {
	return "memory:v1|owner:" + ownerID + "|boundary:" + boundaryID + "|revision:" + revisionID + "|digest:" + digest
}

func encodeIDs(ids []string) (string, error) {
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

func validScore(name string, value float64) error {
	if value < 0 || value > 1 {
		return fmt.Errorf("%s must be between 0 and 1", name)
	}
	return nil
}

func (m *MemoryRecord) Validate() error {
	if strings.TrimSpace(m.OwnerID) == "" || strings.TrimSpace(m.BoundaryID) == "" {
		return fmt.Errorf("owner_id and boundary_id are required")
	}
	if m.Kind != MemoryEvidence && m.Kind != MemoryAssertion && m.Kind != MemoryBelief && m.Kind != MemoryResource {
		return fmt.Errorf("invalid memory kind %q", m.Kind)
	}
	if m.Status == "" {
		m.Status = MemoryActive
	}
	validStatus := map[MemoryStatus]bool{MemoryActive: true, MemorySuperseded: true, MemoryDisputed: true, MemoryRetracted: true, MemoryDeleted: true, MemoryExpired: true}
	if !validStatus[m.Status] {
		return fmt.Errorf("invalid memory status %q", m.Status)
	}
	if len(m.Content) == 0 {
		return fmt.Errorf("content is required")
	}
	actual := memoryDigest(m.Content)
	if m.ContentDigest == "" {
		m.ContentDigest = actual
	} else if m.ContentDigest != actual {
		return fmt.Errorf("content_digest does not match content")
	}
	if len(m.SPIFEnvelope) == 0 {
		return fmt.Errorf("spif_envelope is required")
	}
	for _, s := range []struct {
		name  string
		value float64
	}{{"confidence", m.Confidence}, {"freshness", m.Freshness}, {"source_reliability", m.SourceReliability}, {"ambiguity", m.Ambiguity}, {"disagreement", m.Disagreement}} {
		if err := validScore(s.name, s.value); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) InsertMemoryRecord(m *MemoryRecord) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.RevisionID == "" {
		m.RevisionID = uuid.New().String()
	}
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if m.CapturedAt == "" {
		m.CapturedAt = m.CreatedAt
	}
	if err := m.Validate(); err != nil {
		return err
	}
	key, _, err := security.GetOrCreateKey()
	if err != nil {
		return fmt.Errorf("memory encryption key: %w", err)
	}
	sealed, err := security.EncryptBound(key, string(m.Content), memoryAAD(m.OwnerID, m.BoundaryID, m.RevisionID, m.ContentDigest))
	if err != nil {
		return fmt.Errorf("encrypt memory content: %w", err)
	}
	parents, err := encodeIDs(m.ParentRevisionIDs)
	if err != nil {
		return err
	}
	supporting, err := encodeIDs(m.SupportingIDs)
	if err != nil {
		return err
	}
	contradicting, err := encodeIDs(m.ContradictingIDs)
	if err != nil {
		return err
	}
	_, err = d.conn.Exec(`INSERT INTO memory_records
		(id,owner_id,boundary_id,kind,revision_id,status,content,content_digest,source_actor,source_type,source_ref,captured_at,observed_at,effective_from,effective_until,parent_revision_ids,supporting_ids,contradicting_ids,derivation_id,confidence,freshness,source_reliability,ambiguity,disagreement,sensitivity,created_at,supersedes_revision_id,artifact_id,spif_envelope)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.OwnerID, m.BoundaryID, m.Kind, m.RevisionID, m.Status, []byte(sealed), m.ContentDigest, m.SourceActor, m.SourceType, m.SourceRef, m.CapturedAt, m.ObservedAt, m.EffectiveFrom, m.EffectiveUntil, parents, supporting, contradicting, m.DerivationID, m.Confidence, m.Freshness, m.SourceReliability, m.Ambiguity, m.Disagreement, m.Sensitivity, m.CreatedAt, m.SupersedesRevisionID, m.ArtifactID, m.SPIFEnvelope)
	return err
}

func (d *DB) InsertCorrection(c *CorrectionEvent) error {
	if c.CorrectionID == "" {
		c.CorrectionID = uuid.New().String()
	}
	if c.CreatedAt == "" {
		c.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if c.OwnerID == "" || c.BoundaryID == "" || c.TargetRevisionID == "" || c.ActorID == "" || c.Action == "" || c.ReasonCode == "" || len(c.SPIFEnvelope) == 0 {
		return fmt.Errorf("correction requires owner, boundary, target, actor, action, reason, and spif_envelope")
	}
	validActions := map[string]bool{"amend": true, "retract": true, "narrow_scope": true, "broaden_scope": true, "mark_uncertain": true, "restore": true, "delete": true}
	if !validActions[c.Action] {
		return fmt.Errorf("invalid correction action %q", c.Action)
	}
	evidence, err := encodeIDs(c.EvidenceIDs)
	if err != nil {
		return err
	}
	var owner, boundary string
	if err := d.conn.QueryRow(`SELECT owner_id,boundary_id FROM memory_records WHERE revision_id=?`, c.TargetRevisionID).Scan(&owner, &boundary); err != nil {
		return fmt.Errorf("target revision: %w", err)
	}
	if owner != c.OwnerID || boundary != c.BoundaryID {
		return fmt.Errorf("correction crosses owner or boundary")
	}
	if c.ReplacementRevisionID != "" {
		var replacementBoundary string
		if err := d.conn.QueryRow(`SELECT owner_id,boundary_id FROM memory_records WHERE revision_id=?`, c.ReplacementRevisionID).Scan(&owner, &replacementBoundary); err != nil {
			return fmt.Errorf("replacement revision: %w", err)
		}
		if owner != c.OwnerID || replacementBoundary != c.BoundaryID {
			return fmt.Errorf("replacement crosses owner or boundary")
		}
	}
	_, err = d.conn.Exec(`INSERT INTO memory_corrections (correction_id,owner_id,boundary_id,target_revision_id,replacement_revision_id,action,reason_code,explanation,actor_id,created_at,evidence_ids,spif_envelope) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, c.CorrectionID, c.OwnerID, c.BoundaryID, c.TargetRevisionID, c.ReplacementRevisionID, c.Action, c.ReasonCode, c.Explanation, c.ActorID, c.CreatedAt, evidence, c.SPIFEnvelope)
	return err
}

func (d *DB) InsertRetrievalEvent(r *RetrievalEvent) error {
	if r.RetrievalID == "" {
		r.RetrievalID = uuid.New().String()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if r.Outcome == "" {
		r.Outcome = "unknown"
	}
	valid := map[string]bool{"accepted": true, "rejected": true, "corrected": true, "ignored": true, "harmful": true, "unknown": true}
	if r.OwnerID == "" || r.BoundaryID == "" || r.QueryDigest == "" || r.RankingVersion == "" {
		return fmt.Errorf("retrieval requires owner, boundary, query_digest, and ranking_version")
	}
	if !valid[r.Outcome] {
		return fmt.Errorf("invalid retrieval outcome %q", r.Outcome)
	}
	candidates, err := encodeIDs(r.CandidateRevisionIDs)
	if err != nil {
		return err
	}
	selected, err := encodeIDs(r.SelectedRevisionIDs)
	if err != nil {
		return err
	}
	_, err = d.conn.Exec(`INSERT INTO retrieval_events (retrieval_id,owner_id,boundary_id,query_digest,candidate_revision_ids,selected_revision_ids,ranking_version,feature_snapshot_digest,created_at,outcome,outcome_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, r.RetrievalID, r.OwnerID, r.BoundaryID, r.QueryDigest, candidates, selected, r.RankingVersion, r.FeatureSnapshotDigest, r.CreatedAt, r.Outcome, r.OutcomeAt)
	return err
}

func (d *DB) MemoryRevision(ownerID, boundaryID, revisionID string) (*MemoryRecord, error) {
	var m MemoryRecord
	var kind, status, parents, supporting, contradicting string
	err := d.conn.QueryRow(`SELECT id,owner_id,boundary_id,kind,revision_id,status,content,content_digest,source_actor,source_type,source_ref,captured_at,observed_at,effective_from,effective_until,parent_revision_ids,supporting_ids,contradicting_ids,derivation_id,confidence,freshness,source_reliability,ambiguity,disagreement,sensitivity,created_at,supersedes_revision_id,artifact_id,spif_envelope FROM memory_records WHERE owner_id=? AND boundary_id=? AND revision_id=?`, ownerID, boundaryID, revisionID).Scan(&m.ID, &m.OwnerID, &m.BoundaryID, &kind, &m.RevisionID, &status, &m.Content, &m.ContentDigest, &m.SourceActor, &m.SourceType, &m.SourceRef, &m.CapturedAt, &m.ObservedAt, &m.EffectiveFrom, &m.EffectiveUntil, &parents, &supporting, &contradicting, &m.DerivationID, &m.Confidence, &m.Freshness, &m.SourceReliability, &m.Ambiguity, &m.Disagreement, &m.Sensitivity, &m.CreatedAt, &m.SupersedesRevisionID, &m.ArtifactID, &m.SPIFEnvelope)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.Kind, m.Status = MemoryKind(kind), MemoryStatus(status)
	key, _, keyErr := security.GetOrCreateKey()
	if keyErr != nil {
		return nil, fmt.Errorf("memory decryption key: %w", keyErr)
	}
	plain, decryptErr := security.DecryptBound(key, string(m.Content), memoryAAD(m.OwnerID, m.BoundaryID, m.RevisionID, m.ContentDigest))
	if decryptErr != nil {
		return nil, fmt.Errorf("memory content authentication failed: %w", decryptErr)
	}
	m.Content = []byte(plain)
	if err := json.Unmarshal([]byte(parents), &m.ParentRevisionIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(supporting), &m.SupportingIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(contradicting), &m.ContradictingIDs); err != nil {
		return nil, err
	}
	return &m, nil
}
