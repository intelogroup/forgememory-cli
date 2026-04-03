package db

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AllowedConcepts is the whitelist of concept tags for principles.
// Matches forgememo's concept whitelist to keep tags consistent.
var AllowedConcepts = map[string]bool{
	"security":    true,
	"pattern":     true,
	"gotcha":      true,
	"performance": true,
	"trade-off":   true,
	"how-it-works": true,
}

// Principle represents a distilled high-level insight.
type Principle struct {
	ID            string   `json:"id"`
	TS            string   `json:"ts"`
	Type          string   `json:"type"` // architecture/bugfix/pattern/preference
	Title         string   `json:"title"`
	Narrative     string   `json:"narrative"`
	ImpactScore   float64  `json:"impact_score"`
	ProjectID     string   `json:"project_id"`
	SourceEvent   string   `json:"source_event,omitempty"`
	Fingerprint   string   `json:"fingerprint"`
	Concepts      []string `json:"concepts,omitempty"`
	FilesModified []string `json:"files_modified,omitempty"`
}

// FilterConcepts returns only the concepts that are in AllowedConcepts.
func FilterConcepts(concepts []string) []string {
	var out []string
	for _, c := range concepts {
		c = strings.ToLower(strings.TrimSpace(c))
		if AllowedConcepts[c] {
			out = append(out, c)
		}
	}
	return out
}

// InsertPrinciple stores a new principle.
func (d *DB) InsertPrinciple(p *Principle) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.TS == "" {
		p.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if p.Fingerprint == "" {
		p.Fingerprint = fingerprint(p.Title, p.ProjectID)
	}
	conceptsJSON := encodeStringSlice(p.Concepts)
	filesJSON := encodeStringSlice(p.FilesModified)

	_, err := d.conn.Exec(
		`INSERT OR IGNORE INTO principles
		 (id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint, concepts, files_modified)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TS, p.Type, p.Title, p.Narrative, p.ImpactScore,
		p.ProjectID, p.SourceEvent, p.Fingerprint, conceptsJSON, filesJSON,
	)
	return err
}

// RecentPrinciples returns the most recent principles.
func (d *DB) RecentPrinciples(limit int) ([]Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,'')
		 FROM principles ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var principles []Principle
	for rows.Next() {
		var p Principle
		var conceptsJSON, filesJSON string
		if err := rows.Scan(&p.ID, &p.TS, &p.Type, &p.Title, &p.Narrative,
			&p.ImpactScore, &p.ProjectID, &p.SourceEvent, &p.Fingerprint,
			&conceptsJSON, &filesJSON); err != nil {
			return nil, err
		}
		p.Concepts = decodeStringSlice(conceptsJSON)
		p.FilesModified = decodeStringSlice(filesJSON)
		principles = append(principles, p)
	}
	return principles, rows.Err()
}

// PrincipleCount returns total principle count.
func (d *DB) PrincipleCount() (int, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM principles").Scan(&count)
	return count, err
}

func fingerprint(title, project string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", title, project)))
	return fmt.Sprintf("%x", h[:8])
}

func encodeStringSlice(s []string) string {
	if len(s) == 0 {
		return ""
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeStringSlice(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out
}
