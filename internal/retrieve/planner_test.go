package retrieve

import (
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestEnqueuePromptRetrieval_QueuesContext7ForRustPrompt(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := EnqueuePromptRetrieval(database, "api-service", "cargo build fails with rust error E0599, show the official rust docs"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval: %v", err)
	}
	if err := EnqueuePromptRetrieval(database, "api-service", "cargo build fails with rust error E0599, show the official rust docs"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval duplicate: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1 deduped job", len(jobs))
	}
	if jobs[0].Source != "context7" {
		t.Fatalf("Source = %q, want context7", jobs[0].Source)
	}
	if jobs[0].LibraryName != "rust" {
		t.Fatalf("LibraryName = %q, want rust", jobs[0].LibraryName)
	}
}

func TestEnqueuePromptRetrieval_QueuesContext7SetupForInstallPrompt(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := EnqueuePromptRetrieval(database, "forgememory-cli", "show me the context7 local mcp setup for codex"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("forgememory-cli", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if jobs[0].Source != "context7_setup" {
		t.Fatalf("Source = %q, want context7_setup", jobs[0].Source)
	}
	if jobs[0].LibraryName != "context7" {
		t.Fatalf("LibraryName = %q, want context7", jobs[0].LibraryName)
	}
}

func TestEnqueuePromptRetrieval_QueuesOpenSrcForSourceIntent(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := EnqueuePromptRetrieval(database, "web-app", "inspect the next.js source implementation and docs for router internals"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("web-app", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2 jobs", len(jobs))
	}

	sources := map[string]bool{}
	for _, job := range jobs {
		sources[job.Source] = true
	}
	if !sources["context7"] || !sources["opensrc"] {
		t.Fatalf("expected context7 and opensrc jobs, got %#v", jobs)
	}
}

func TestEnqueuePromptRetrieval_DoesNotQueueGenericPrompt(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := EnqueuePromptRetrieval(database, "forgememory-cli", "refactor the daemon polling loop"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("forgememory-cli", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("len(jobs) = %d, want 0", len(jobs))
	}
}

func TestEnqueueFailureRetrieval_QueuesContext7ForRepeatedRustFailure(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	payload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState"}}`
	if err := EnqueueFailureRetrieval(database, "api-service", "cargo build", "rust:e0599", "error[e0599]: no method named serve found for struct appstate", payload); err != nil {
		t.Fatalf("EnqueueFailureRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if jobs[0].TriggerType != "failure" {
		t.Fatalf("TriggerType = %q, want failure", jobs[0].TriggerType)
	}
	if jobs[0].Source != "context7" {
		t.Fatalf("Source = %q, want context7", jobs[0].Source)
	}
	if jobs[0].LibraryName != "rust" {
		t.Fatalf("LibraryName = %q, want rust", jobs[0].LibraryName)
	}
	if jobs[0].Priority != 100 {
		t.Fatalf("Priority = %d, want 100", jobs[0].Priority)
	}
}
