package artifacts

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestStorePutDeduplicatesContentAndRedactsText(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "artifacts.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	store := Store{DB: database, Root: filepath.Join(t.TempDir(), "evidence")}

	content := []byte("token=sk-proj-12345678901234567890\nPASS")
	one, err := store.Put("trace-1", "task-1", "test-report", "text/plain", content, "")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	two, err := store.Put("trace-1", "task-1", "test-report", "text/plain", content, "")
	if err != nil {
		t.Fatalf("retry Put: %v", err)
	}
	if one.ID != two.ID || one.SHA256 != two.SHA256 || one.ByteSize != two.ByteSize {
		t.Fatalf("retries differ: %#v %#v", one, two)
	}
	reader, err := store.Open(one)
	if err != nil {
		t.Fatalf("Open artifact: %v", err)
	}
	stored, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || strings.Contains(string(stored), "sk-proj-") || !strings.Contains(string(stored), "REDACTED") {
		t.Fatalf("stored content = %q, err=%v", stored, err)
	}
	rows, err := database.EvaluationArtifacts("trace-1", "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("metadata rows = %#v, err=%v", rows, err)
	}
}

func TestStoreRejectsUnsupportedKind(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "artifacts.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	_, err = (Store{DB: database, Root: t.TempDir()}).Put("trace-1", "", "secret-dump", "text/plain", []byte("x"), "")
	if err == nil {
		t.Fatal("Put accepted unsupported artifact kind")
	}
}
