package retrieve

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestEnqueuePromptRetrieval_QueuesContext7ForRustPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_EXA_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FORGE_TAVILY_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")

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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_EXA_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FORGE_TAVILY_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")

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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_EXA_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FORGE_TAVILY_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")

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

	// No web search key set — only context7 should be queued.
	t.Setenv("FORGE_EXA_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FORGE_TAVILY_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	payload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState"}}`
	if err := EnqueueFailureRetrieval(database, "api-service", "cargo build", "rust:e0599", "error[e0599]: no method named serve found for struct appstate", payload); err != nil {
		t.Fatalf("EnqueueFailureRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1 (context7 only)", len(jobs))
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

func TestEnqueueFailureRetrieval_QueuesWebSearchWhenExaKeySet(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_EXA_API_KEY", "test-exa-key")

	payload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0412]: cannot find type AppState in this scope"}}`
	if err := EnqueueFailureRetrieval(database, "api-service", "cargo build", "rustc", "error[e0412]: cannot find type appstate in this scope", payload); err != nil {
		t.Fatalf("EnqueueFailureRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2 (context7 + exa)", len(jobs))
	}

	sources := map[string]int{}
	for _, job := range jobs {
		sources[job.Source] = job.Priority
	}
	if _, ok := sources["context7"]; !ok {
		t.Fatalf("expected context7 job, got sources %v", sources)
	}
	if _, ok := sources["exa"]; !ok {
		t.Fatalf("expected exa job, got sources %v", sources)
	}
	if sources["context7"] != 100 {
		t.Fatalf("context7 priority = %d, want 100", sources["context7"])
	}
	if sources["exa"] != 85 {
		t.Fatalf("exa priority = %d, want 85", sources["exa"])
	}
}

func TestEnqueuePromptRetrieval_QueuesWebSearchAlongsideContext7(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_EXA_API_KEY", "test-exa-key")

	if err := EnqueuePromptRetrieval(database, "api-service", "cargo build fails with rust error E0599, show the official rust docs"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2 (context7 + exa)", len(jobs))
	}

	sources := map[string]bool{}
	for _, job := range jobs {
		sources[job.Source] = true
	}
	if !sources["context7"] {
		t.Fatalf("expected context7 job, got %v", sources)
	}
	if !sources["exa"] {
		t.Fatalf("expected exa job, got %v", sources)
	}
}

func TestBuildWebQuery_AddsYearAndFixForFailure(t *testing.T) {
	job := &db.RetrievalJob{
		TriggerType: "failure",
		Query:       "rust cargo build error E0412 cannot find type",
		LibraryName: "rust",
	}
	q := buildWebQuery(job)
	if !strings.Contains(q, "2025") {
		t.Fatalf("buildWebQuery %q should contain 2025", q)
	}
	if !strings.Contains(q, "fix") {
		t.Fatalf("buildWebQuery %q should contain 'fix'", q)
	}
}

func TestBuildWebQuery_AddsBestPracticeForPrompt(t *testing.T) {
	job := &db.RetrievalJob{
		TriggerType: "prompt",
		Query:       "react server components data fetching",
		LibraryName: "react",
	}
	q := buildWebQuery(job)
	if !strings.Contains(q, "best practice") {
		t.Fatalf("buildWebQuery %q should contain 'best practice'", q)
	}
}

func TestEnqueueFailureRetrieval_SkipsExternalWhenRelevantPrincipleExists(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_EXA_API_KEY", "test-exa-key")

	// Insert a cross-project principle that matches rust + e0599.
	if _, err := database.InsertPrinciple(&db.Principle{
		Type:        "bugfix",
		Title:       "Rust E0599: import the trait before calling its methods",
		Narrative:   "When cargo reports E0599, the method exists on a trait that is not in scope. Add the use statement for the trait.",
		ImpactScore: 0.85,
		ProjectID:   "other-project",
		Concepts:    []string{"gotcha"},
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	payload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found"}}`
	if err := EnqueueFailureRetrieval(database, "api-service", "cargo build", "rustc", "error[e0599]: no method named serve found", payload); err != nil {
		t.Fatalf("EnqueueFailureRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("len(jobs) = %d, want 0 — principle gate should have blocked external fetch", len(jobs))
	}
}

func TestEnqueueFailureRetrieval_SkipsWebSearchWhenFreshSummaryExists(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_EXA_API_KEY", "test-exa-key")
	t.Setenv("FORGE_EXA_BASE_URL", "http://unused")
	t.Setenv("HOME", t.TempDir())

	// Pre-populate a fresh exa summary for rust in this project.
	if err := database.UpsertExternalContextSummary(&db.ExternalContextSummary{
		Source:      "exa",
		ProjectID:   "api-service",
		LibraryName: "rust",
		Query:       "rust cargo build fix 2025",
		Title:       "exa rust",
		Narrative:   "Use the async-trait crate for object-safe async traits.",
		TrustScore:  0.68,
		ExpiresAt:   "2099-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertExternalContextSummary: %v", err)
	}

	payload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0412]: cannot find type AppState"}}`
	if err := EnqueueFailureRetrieval(database, "api-service", "cargo build", "rustc", "error[e0412]: cannot find type appstate", payload); err != nil {
		t.Fatalf("EnqueueFailureRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	// Only context7 should be enqueued; exa is skipped due to fresh cached summary.
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1 (context7 only, exa skipped)", len(jobs))
	}
	if jobs[0].Source != "context7" {
		t.Fatalf("Source = %q, want context7", jobs[0].Source)
	}
}

func TestRelevantPrincipleExists_RequiresLibraryPlusOneOtherTerm(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Principle matches library but NOT any error-specific term.
	if _, err := database.InsertPrinciple(&db.Principle{
		Type:        "pattern",
		Title:       "Rust: prefer iterators over manual loops",
		Narrative:   "Using rust iterator adaptors produces cleaner and often faster code.",
		ImpactScore: 0.7,
		ProjectID:   "other-project",
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	// errorKind and normalizedMessage have no overlap with the principle above.
	if relevantPrincipleExists(database, "rust", "rustc", "error[e0499]: cannot borrow as mutable more than once") {
		t.Fatal("should not match — no error-specific term overlap")
	}
}

func TestBuildWebQuery_StripsContext7Boilerplate(t *testing.T) {
	job := &db.RetrievalJob{
		TriggerType: "failure",
		Query:       "rust cargo build rustc error official docs explanation and fix in one short sentence",
		LibraryName: "rust",
	}
	q := buildWebQuery(job)
	if strings.Contains(q, "official docs explanation") {
		t.Fatalf("buildWebQuery should strip context7 boilerplate, got %q", q)
	}
}

func TestEnqueuePromptRetrieval_DoesNotQueueExaWhenUnconfigured(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_EXA_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FORGE_TAVILY_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")

	if err := EnqueuePromptRetrieval(database, "api-service", "cargo build fails with rust error E0599, show the official rust docs"); err != nil {
		t.Fatalf("EnqueuePromptRetrieval: %v", err)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 10)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	// It should only queue context7, not exa!
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1 (context7 only)", len(jobs))
	}
	if jobs[0].Source != "context7" {
		t.Fatalf("expected context7 job, got %s", jobs[0].Source)
	}
}
