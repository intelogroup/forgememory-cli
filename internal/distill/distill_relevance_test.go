package distill

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestGetRelevantPrinciples(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer database.Close()

	// Insert test principles
	p1 := &db.Principle{
		ID:            "p1",
		TS:            time.Now().UTC().Format(time.RFC3339),
		Type:          "pattern",
		Title:         "Avoid global variables",
		Narrative:     "Global state variables cause concurrency bugs and race conditions in tests.",
		ImpactScore:   0.8,
		ProjectID:     "my-project",
		FilesModified: []string{"src/main.go", "src/state.go"},
	}
	p2 := &db.Principle{
		ID:            "p2",
		TS:            time.Now().UTC().Format(time.RFC3339),
		Type:          "gotcha",
		Title:         "SQLite busy lock handling",
		Narrative:     "Always set busy_timeout and use read-only mode for read-only database operations.",
		ImpactScore:   0.9,
		ProjectID:     "my-project",
		FilesModified: []string{"internal/db.go"},
	}
	p3 := &db.Principle{
		ID:            "p3",
		TS:            time.Now().UTC().Format(time.RFC3339),
		Type:          "performance",
		Title:         "Use redis caching",
		Narrative:     "Cache expensive network calls to Redis with appropriate TTL.",
		ImpactScore:   0.7,
		ProjectID:     "other-project", // cross-project
		FilesModified: []string{"src/cache.go"},
	}

	for _, p := range []*db.Principle{p1, p2, p3} {
		if _, err := database.InsertPrinciple(p); err != nil {
			t.Fatalf("failed to insert principle %s: %v", p.ID, err)
		}
	}

	t.Run("Recency-based matching", func(t *testing.T) {
		principles, err := GetRelevantPrinciples(database, "my-project", "", nil)
		if err != nil {
			t.Fatalf("GetRelevantPrinciples failed: %v", err)
		}
		if len(principles) != 2 {
			t.Fatalf("expected 2 principles, got %d", len(principles))
		}
		// Ordered by decayed score/TS
		if principles[0].ID != "p2" || principles[1].ID != "p1" {
			t.Errorf("unexpected order of principles: %v", principles)
		}
	})

	t.Run("FTS5 Query Hint match (cross-project)", func(t *testing.T) {
		// Should pull p3 because of FTS match on "redis caching"
		principles, err := GetRelevantPrinciples(database, "my-project", "caching strategy", nil)
		if err != nil {
			t.Fatalf("GetRelevantPrinciples failed: %v", err)
		}
		// Should have p1, p2, and now p3
		foundP3 := false
		for _, p := range principles {
			if p.ID == "p3" {
				foundP3 = true
				break
			}
		}
		if !foundP3 {
			t.Error("expected to find cross-project principle p3 matching the query hint")
		}
	})

	t.Run("Active files Jaccard boost", func(t *testing.T) {
		// activeFiles matches "src/state.go" (Jaccard with p1: 1/2, with p2: 0)
		// p1 score: 0.8 + 0.3 * (1/2) = 0.95
		// p2 score: 0.9 + 0 = 0.90
		// So p1 should rank higher than p2 now
		principles, err := GetRelevantPrinciples(database, "my-project", "", []string{"src/state.go"})
		if err != nil {
			t.Fatalf("GetRelevantPrinciples failed: %v", err)
		}
		if len(principles) < 2 {
			t.Fatalf("expected at least 2 principles, got %d", len(principles))
		}
		if principles[0].ID != "p1" {
			t.Errorf("expected p1 to be boosted to top, got %s", principles[0].ID)
		}
	})
}

func TestJaccardIntersection(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected float64
	}{
		{
			name:     "empty lists",
			a:        nil,
			b:        []string{"a"},
			expected: 0.0,
		},
		{
			name:     "exact match",
			a:        []string{"src/main.go", "src/db.go"},
			b:        []string{"src/main.go", "src/db.go"},
			expected: 1.0,
		},
		{
			name:     "partial match",
			a:        []string{"src/main.go"},
			b:        []string{"src/main.go", "src/db.go"},
			expected: 0.5, // 1 intersection / 2 union
		},
		{
			name:     "case and path clean normalization",
			a:        []string{".\\src\\main.go"},
			b:        []string{"src/MAIN.go", "src/db.go"},
			expected: 0.5,
		},
		{
			name:     "no overlap",
			a:        []string{"a.go"},
			b:        []string{"b.go"},
			expected: 0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := jaccardIntersection(tc.a, tc.b)
			if got != tc.expected {
				t.Errorf("jaccardIntersection(%v, %v) = %f, want %f", tc.a, tc.b, got, tc.expected)
			}
		})
	}
}

func TestOpenReadOnly(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// 1. Should fail if file doesn't exist
	_, err := db.OpenReadOnly(dbPath)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected error for non-existent db file, got: %v", err)
	}

	// 2. Create the db first
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	database.Close()

	// 3. Open in read-only mode should succeed
	roDB, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly failed: %v", err)
	}
	defer roDB.Close()

	// Verify we cannot write to it
	_, err = roDB.Conn().Exec("INSERT INTO principles(id, ts, type, title, narrative) VALUES('test', 'ts', 'type', 'title', 'narr narrative')")
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Errorf("expected write to fail on read-only DB connection, got error: %v", err)
	}
}
