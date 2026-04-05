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
