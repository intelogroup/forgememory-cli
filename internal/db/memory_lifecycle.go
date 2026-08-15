package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/forge/forge/internal/security"
)

type MemorySearchResult struct {
	Record MemoryRecord `json:"record"`
	Score  float64      `json:"score"`
	Reason string       `json:"reason"`
}

// SearchMemory applies owner/boundary/status authorization before inspecting
// content. This ordering is an invariant: restricted rows cannot influence
// ranking or leak through snippets.
func (d *DB) SearchMemory(ownerID, boundaryID, query string, limit int) ([]MemorySearchResult, error) {
	if ownerID == "" || boundaryID == "" {
		return nil, fmt.Errorf("owner and boundary are required")
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.conn.Query(`SELECT revision_id FROM memory_records WHERE owner_id=? AND boundary_id=? AND status='active' ORDER BY created_at DESC`, ownerID, boundaryID)
	if err != nil {
		return nil, err
	}
	var revisionIDs []string
	for rows.Next() {
		var revisionID string
		if err := rows.Scan(&revisionID); err != nil {
			rows.Close()
			return nil, err
		}
		revisionIDs = append(revisionIDs, revisionID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	results := make([]MemorySearchResult, 0, limit)
	for _, revisionID := range revisionIDs {
		m, err := d.MemoryRevision(ownerID, boundaryID, revisionID)
		if err != nil {
			return nil, err
		}
		if m == nil || m.Status != MemoryActive || (query != "" && !strings.Contains(strings.ToLower(string(m.Content)), query)) {
			continue
		}
		score := m.Confidence + m.Freshness*0.25 - m.Disagreement*0.25
		results = append(results, MemorySearchResult{Record: *m, Score: score, Reason: "authorized active revision; matched content and uncertainty score"})
		if len(results) == limit {
			break
		}
	}
	return results, nil
}

func (d *DB) DeleteMemoryRevision(ownerID, boundaryID, revisionID string) error {
	result, err := d.conn.Exec(`UPDATE memory_records SET status='deleted', content=? WHERE owner_id=? AND boundary_id=? AND revision_id=?`, []byte{}, ownerID, boundaryID, revisionID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("memory revision not found")
	}
	return nil
}

type MemoryBundle struct {
	Version     string            `json:"version"`
	OwnerID     string            `json:"owner_id"`
	BoundaryID  string            `json:"boundary_id"`
	Records     []MemoryRecord    `json:"records"`
	Corrections []CorrectionEvent `json:"corrections"`
}

func (d *DB) ExportMemoryBundle(ownerID, boundaryID string) ([]byte, error) {
	rows, err := d.conn.Query(`SELECT revision_id FROM memory_records WHERE owner_id=? AND boundary_id=? ORDER BY created_at`, ownerID, boundaryID)
	if err != nil {
		return nil, err
	}
	var revisionIDs []string
	for rows.Next() {
		var revisionID string
		if err := rows.Scan(&revisionID); err != nil {
			rows.Close()
			return nil, err
		}
		revisionIDs = append(revisionIDs, revisionID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	bundle := MemoryBundle{Version: "forge-memory-bundle-v1", OwnerID: ownerID, BoundaryID: boundaryID, Records: []MemoryRecord{}, Corrections: []CorrectionEvent{}}
	for _, revisionID := range revisionIDs {
		m, err := d.MemoryRevision(ownerID, boundaryID, revisionID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			bundle.Records = append(bundle.Records, *m)
		}
	}
	return json.Marshal(bundle)
}

func (d *DB) ImportMemoryBundle(raw []byte) error {
	var bundle MemoryBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return fmt.Errorf("decode memory bundle: %w", err)
	}
	if bundle.Version != "forge-memory-bundle-v1" || bundle.OwnerID == "" || bundle.BoundaryID == "" {
		return fmt.Errorf("invalid memory bundle")
	}
	for i := range bundle.Records {
		m := bundle.Records[i]
		if m.OwnerID != bundle.OwnerID || m.BoundaryID != bundle.BoundaryID {
			return fmt.Errorf("bundle record crosses scope")
		}
		if err := d.InsertMemoryRecord(&m); err != nil {
			return err
		}
	}
	for i := range bundle.Corrections {
		c := bundle.Corrections[i]
		if c.OwnerID != bundle.OwnerID || c.BoundaryID != bundle.BoundaryID {
			return fmt.Errorf("bundle correction crosses scope")
		}
		if err := d.InsertCorrection(&c); err != nil {
			return err
		}
	}
	return nil
}

func memoryCiphertextPresent(digest string, content []byte) bool {
	return digest != "" && len(content) > 0 && bytes.HasPrefix(content, []byte(security.EncPrefix))
}
