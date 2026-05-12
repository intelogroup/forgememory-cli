package detect

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestProcessEvent_RaisesAlertOnThirdRepeatedFailure(t *testing.T) {
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

	payload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		event := &db.Event{
			TS:         time.Now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			SessionID:  "sess-1",
			ProjectID:  "api-service",
			SourceTool: "codex",
			EventType:  "PostToolUse",
			ToolName:   "Bash",
			Payload:    payload,
		}
		if err := ProcessEvent(database, event); err != nil {
			t.Fatalf("ProcessEvent: %v", err)
		}
	}

	alerts, err := database.ActiveAlertsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(alerts))
	}
	if alerts[0].AlertType != "repeated_failure" {
		t.Fatalf("AlertType = %q, want repeated_failure", alerts[0].AlertType)
	}
	if alerts[0].Severity != "medium" {
		t.Fatalf("Severity = %q, want medium", alerts[0].Severity)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1 retrieval job", len(jobs))
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
}

func TestProcessEvent_DoesNotRaiseAlertBeforeThirdFailure(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	payload := `{"tool_input":{"command":"cargo test"},"tool_response":{"stderr":"error[E0432]: unresolved import crate::server"}}`
	for i := 0; i < 2; i++ {
		event := &db.Event{
			TS:         time.Now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			SessionID:  "sess-2",
			ProjectID:  "api-service",
			SourceTool: "codex",
			EventType:  "PostToolUse",
			ToolName:   "Bash",
			Payload:    payload,
		}
		if err := ProcessEvent(database, event); err != nil {
			t.Fatalf("ProcessEvent: %v", err)
		}
	}

	alerts, err := database.ActiveAlertsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("len(alerts) = %d, want 0", len(alerts))
	}
}

func TestObserveFailure_NormalizesPathsAndLineNumbersIntoStableFingerprint(t *testing.T) {
	eventA := &db.Event{
		SessionID:  "sess-3",
		ProjectID:  "api-service",
		SourceTool: "codex",
		ToolName:   "Bash",
		Payload:    `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found in /tmp/project/src/main.rs:42:9"}}`,
	}
	eventB := &db.Event{
		SessionID:  "sess-3",
		ProjectID:  "api-service",
		SourceTool: "codex",
		ToolName:   "Bash",
		Payload:    `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found in /Users/dev/work/api/src/main.rs:118:13"}}`,
	}

	obsA := observeFailure(eventA)
	obsB := observeFailure(eventB)
	if obsA == nil || obsB == nil {
		t.Fatalf("expected both observations to be detected, got %#v and %#v", obsA, obsB)
	}
	if obsA.Fingerprint != obsB.Fingerprint {
		t.Fatalf("fingerprints differ for normalized equivalent failures: %q vs %q", obsA.Fingerprint, obsB.Fingerprint)
	}
}

func TestObserveFailure_IgnoresSuccessOnlyPayloads(t *testing.T) {
	event := &db.Event{
		SessionID:  "sess-4",
		ProjectID:  "api-service",
		SourceTool: "codex",
		ToolName:   "Bash",
		Payload:    `{"tool_input":{"command":"cargo build"},"tool_response":{"stdout":"Finished dev profile target(s) in 1.22s\nBuild succeeded successfully"}}`,
	}

	if obs := observeFailure(event); obs != nil {
		t.Fatalf("expected success-only payload to be ignored, got %#v", obs)
	}
}

func TestProcessEvent_SeparatesDifferentFailureFingerprints(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	payloads := []string{
		`{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState"}}`,
		`{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0432]: unresolved import crate::server"}}`,
	}
	for _, payload := range payloads {
		for i := 0; i < 2; i++ {
			event := &db.Event{
				TS:         time.Now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
				SessionID:  "sess-5",
				ProjectID:  "api-service",
				SourceTool: "codex",
				EventType:  "PostToolUse",
				ToolName:   "Bash",
				Payload:    payload,
			}
			if err := ProcessEvent(database, event); err != nil {
				t.Fatalf("ProcessEvent: %v", err)
			}
		}
	}

	alerts, err := database.ActiveAlertsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alert when two distinct failures repeat only twice each, got %#v", alerts)
	}
}

func TestProcessEvent_EscalatesSeverityOnFourthRepeat(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	payload := `{"tool_input":{"command":"npm install"},"tool_response":{"stderr":"npm ERR! code ERESOLVE\nnpm ERR! unable to resolve dependency tree"}}`
	for i := 0; i < 4; i++ {
		event := &db.Event{
			TS:         time.Now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			SessionID:  "sess-6",
			ProjectID:  "web-app",
			SourceTool: "codex",
			EventType:  "PostToolUse",
			ToolName:   "Bash",
			Payload:    payload,
		}
		if err := ProcessEvent(database, event); err != nil {
			t.Fatalf("ProcessEvent: %v", err)
		}
	}

	alerts, err := database.ActiveAlertsByProject("web-app", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(alerts))
	}
	if alerts[0].Severity != "high" {
		t.Fatalf("Severity = %q, want high", alerts[0].Severity)
	}
	if !strings.Contains(alerts[0].Narrative, "4 times") {
		t.Fatalf("expected updated repeat count in narrative, got %q", alerts[0].Narrative)
	}
}

func TestProcessEvent_ResolvesAlertOnLikelySuccessForSameCommand(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	failPayload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0599]: no method named serve found for struct AppState\ncould not compile api-service due to previous error"}}`
	for i := 0; i < 3; i++ {
		event := &db.Event{
			TS:         time.Now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			SessionID:  "sess-7",
			ProjectID:  "api-service",
			SourceTool: "codex",
			EventType:  "PostToolUse",
			ToolName:   "Bash",
			Payload:    failPayload,
		}
		if err := ProcessEvent(database, event); err != nil {
			t.Fatalf("ProcessEvent failure: %v", err)
		}
	}

	success := &db.Event{
		TS:         time.Now().Add(4 * time.Minute).UTC().Format(time.RFC3339),
		SessionID:  "sess-7",
		ProjectID:  "api-service",
		SourceTool: "codex",
		EventType:  "PostToolUse",
		ToolName:   "Bash",
		Payload:    `{"tool_input":{"command":"cargo build"},"tool_response":{"stdout":"Compiling api-service v0.1.0\nFinished dev profile target(s) in 0.84s"}}`,
	}
	if err := ProcessEvent(database, success); err != nil {
		t.Fatalf("ProcessEvent success: %v", err)
	}

	alerts, err := database.ActiveAlertsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected alert to be cleared by likely success, got %#v", alerts)
	}
}

func TestProcessEvent_DoesNotResolveAlertOnUnrelatedSuccess(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	failPayload := `{"tool_input":{"command":"cargo build"},"tool_response":{"stderr":"error[E0432]: unresolved import crate::server"}}`
	for i := 0; i < 3; i++ {
		event := &db.Event{
			TS:         time.Now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			SessionID:  "sess-8",
			ProjectID:  "api-service",
			SourceTool: "codex",
			EventType:  "PostToolUse",
			ToolName:   "Bash",
			Payload:    failPayload,
		}
		if err := ProcessEvent(database, event); err != nil {
			t.Fatalf("ProcessEvent failure: %v", err)
		}
	}

	success := &db.Event{
		TS:         time.Now().Add(4 * time.Minute).UTC().Format(time.RFC3339),
		SessionID:  "sess-8",
		ProjectID:  "api-service",
		SourceTool: "codex",
		EventType:  "PostToolUse",
		ToolName:   "Bash",
		Payload:    `{"tool_input":{"command":"npm install"},"tool_response":{"stdout":"added 34 packages successfully"}}`,
	}
	if err := ProcessEvent(database, success); err != nil {
		t.Fatalf("ProcessEvent unrelated success: %v", err)
	}

	alerts, err := database.ActiveAlertsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected unresolved alert to remain active, got %#v", alerts)
	}
}

func TestObserveFailure_IgnoresPythonFunctionSignatureInToolInput(t *testing.T) {
	// Claude edits a file whose old_string/new_string contain a function with
	// "errors" and label="error" — must never fire a failure observation.
	payload := `{"tool_input":{"file_path":"cli.py","old_string":"def _print_report(errors, warnings, label=\"error\"):","new_string":"def _print_report(errors, warnings, label=\"error\"):"},"tool_response":{"type":"result","result":"The file has been edited successfully."}}`
	event := &db.Event{
		SessionID:  "sess-fp",
		ProjectID:  "gliffon",
		SourceTool: "claude",
		ToolName:   "Edit",
		Payload:    payload,
	}
	for i := 0; i < 5; i++ {
		if obs := observeFailure(event); obs != nil {
			t.Fatalf("false positive: observeFailure should not flag source code in tool_input, got %+v", obs)
		}
	}
}

func TestClassifyFailure_IgnoresSourceCodeDeclarations(t *testing.T) {
	cases := []string{
		`def _print_report(errors, warnings, label="error"):`,
		`func handleError(err error) error {`,
		`async def on_error(self, exception):`,
		`class ErrorHandler(BaseException):`,
		`public void throwError(String message) {`,
	}
	for _, line := range cases {
		if got := classifyFailure(line); got != "" {
			t.Fatalf("classifyFailure(%q) = %q, want empty — source code declaration should not be flagged", line, got)
		}
	}
}

func TestCommandFamily_IgnoresPathLikeText(t *testing.T) {
	if got := commandFamily("/tmp/api-service"); got != "" {
		t.Fatalf("commandFamily(path) = %q, want empty", got)
	}
	if got := commandFamily(`C:\work\api-service`); got != "" {
		t.Fatalf("commandFamily(windows path) = %q, want empty", got)
	}
}

func TestCommandFamily_RecognizesKnownBuildCommands(t *testing.T) {
	if got := commandFamily("cargo build"); got != "cargo build" {
		t.Fatalf("commandFamily(cargo build) = %q, want cargo build", got)
	}
	if got := commandFamily("npm install"); got != "npm install" {
		t.Fatalf("commandFamily(npm install) = %q, want npm install", got)
	}
}
