package db

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Principle represents a distilled high-level insight.
type Principle struct {
	ID          string  `json:"id"`
	TS          string  `json:"ts"`
	Type        string  `json:"type"` // architecture/bugfix/pattern/preference
	Title       string  `json:"title"`
	Narrative   string  `json:"narrative"`
	ImpactScore float64 `json:"impact_score"`
	ProjectID   string  `json:"project_id"`
	SourceEvent string  `json:"source_event,omitempty"`
	Fingerprint string  `json:"fingerprint"`
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
	// Upsert: skip if fingerprint already exists
	_, err := d.conn.Exec(
		`INSERT OR IGNORE INTO principles (id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TS, p.Type, p.Title, p.Narrative, p.ImpactScore, p.ProjectID, p.SourceEvent, p.Fingerprint,
	)
	return err
}

// RecentPrinciples returns the most recent principles.
func (d *DB) RecentPrinciples(limit int) ([]Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint
		 FROM principles ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var principles []Principle
	for rows.Next() {
		var p Principle
		if err := rows.Scan(&p.ID, &p.TS, &p.Type, &p.Title, &p.Narrative, &p.ImpactScore, &p.ProjectID, &p.SourceEvent, &p.Fingerprint); err != nil {
			return nil, err
		}
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
