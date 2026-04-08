package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestTruncateContent(t *testing.T) {
	short := "abc"
	if got := truncateContent(short, 10); got != short {
		t.Fatalf("truncateContent(short) = %q, want %q", got, short)
	}

	long := strings.Repeat("x", 20)
	got := truncateContent(long, 5)
	if !strings.HasPrefix(got, "xxxxx") {
		t.Fatalf("truncateContent prefix = %q, want xxxxx...", got)
	}
	if !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("truncateContent suffix = %q, missing truncation marker", got)
	}
}

func TestScanGemini_PersistsContentInPayload(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	content := "memory line 1\nmemory line 2"
	if err := os.WriteFile(filepath.Join(home, ".gemini", "GEMINI.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write GEMINI.md failed: %v", err)
	}

	sc := &Scanner{DB: database}
	hashes := hashStore{Hashes: map[string]string{}}
	saved := sc.scanGemini(home, hashes)
	if saved != 1 {
		t.Fatalf("scanGemini saved = %d, want 1", saved)
	}

	events, err := database.RecentEvents(1)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Payload), &payload); err != nil {
		t.Fatalf("payload JSON invalid: %v", err)
	}
	if payload["source"] != "gemini" {
		t.Fatalf("payload source = %v, want gemini", payload["source"])
	}
	if !strings.Contains(payload["content"].(string), "memory line 1") {
		t.Fatalf("payload content missing expected text: %q", payload["content"])
	}
}

func TestPayloadJSON_InvalidMapFallsBackToObject(t *testing.T) {
	ch := make(chan int)
	got := payloadJSON(map[string]any{"bad": ch})
	if got != "{}" {
		t.Fatalf("payloadJSON invalid input = %q, want {}", got)
	}
}

func TestScanGemini_SkipsWhenHashUnchanged(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	geminiPath := filepath.Join(home, ".gemini", "GEMINI.md")
	content := "stable memory content"
	if err := os.WriteFile(geminiPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sc := &Scanner{DB: database}
	hashes := hashStore{Hashes: map[string]string{}}

	// First scan should save.
	if saved := sc.scanGemini(home, hashes); saved != 1 {
		t.Fatalf("first scan: saved = %d, want 1", saved)
	}
	// Second scan with same hash should be a no-op.
	if saved := sc.scanGemini(home, hashes); saved != 0 {
		t.Fatalf("second scan: saved = %d, want 0 (dedup)", saved)
	}
}

func TestScanGemini_SkipsWhenFileAbsent(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sc := &Scanner{DB: database}
	hashes := hashStore{Hashes: map[string]string{}}
	if saved := sc.scanGemini(filepath.Join(tmp, "no-home"), hashes); saved != 0 {
		t.Fatalf("absent file: saved = %d, want 0", saved)
	}
}

func TestLoadSaveHashes_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	original := hashStore{Hashes: map[string]string{
		"file1": "abc123",
		"file2": "def456",
	}}
	if err := saveHashes(original); err != nil {
		t.Fatalf("saveHashes: %v", err)
	}

	loaded, err := loadHashes()
	if err != nil {
		t.Fatalf("loadHashes: %v", err)
	}
	if loaded.Hashes["file1"] != "abc123" {
		t.Fatalf("file1 hash = %q, want abc123", loaded.Hashes["file1"])
	}
	if loaded.Hashes["file2"] != "def456" {
		t.Fatalf("file2 hash = %q, want def456", loaded.Hashes["file2"])
	}
}
