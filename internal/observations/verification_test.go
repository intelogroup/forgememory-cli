package observations

import (
	"fmt"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

const (
	projectID = "project-a"
	sessionID = "session-a"
)

func TestDetectVerification(t *testing.T) {
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	stamp := func(minutes int) string { return base.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339) }

	tests := []struct {
		name           string
		input          Input
		wantKinds      []string
		wantConfidence map[string]float64
		wantSupporting map[string][]string
		wantCounter    map[string][]string
	}{
		{
			name: "targeted passing test after a code write is positive evidence",
			input: Input{ProjectID: projectID, SessionID: sessionID, Live: true, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("targeted", stamp(1), "go test ./internal/payments", 0, "PASS\nok\tforge/internal/payments"),
			}},
			wantKinds:      []string{"verification_detected"},
			wantConfidence: map[string]float64{"verification_detected": 0.75},
			wantSupporting: map[string][]string{"verification_detected": {"change", "targeted"}},
		},
		{
			name: "project-wide node test is broad positive evidence",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "src/api/users.ts"),
				testEvent("broad", stamp(1), "npm test", 0, "Tests: 5 passed"),
			}},
			wantKinds:      []string{"verification_detected"},
			wantConfidence: map[string]float64{"verification_detected": 0.65},
			wantSupporting: map[string][]string{"verification_detected": {"change", "broad"}},
		},
		{
			name: "filtered cargo test without package scope is not broad verification",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "src/auth/login.rs"),
				testEvent("filtered", stamp(1), "cargo test auth", 0, "test result: ok"),
			}},
			wantKinds:   []string{"code_change_without_relevant_test"},
			wantCounter: map[string][]string{"code_change_without_relevant_test": {"filtered"}},
		},
		{
			name: "go test run filter without package scope is not broad verification",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("filtered", stamp(1), "go test -run TestCharge", 0, "PASS"),
			}},
			wantKinds:   []string{"code_change_without_relevant_test"},
			wantCounter: map[string][]string{"code_change_without_relevant_test": {"filtered"}},
		},
		{
			name: "pytest keyword filter without path scope is not broad verification",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "src/auth/login.py"),
				testEvent("filtered", stamp(1), "pytest -k auth", 0, "1 passed"),
			}},
			wantKinds:   []string{"code_change_without_relevant_test"},
			wantCounter: map[string][]string{"code_change_without_relevant_test": {"filtered"}},
		},
		{
			name: "child package tests do not verify a parent package change",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("child", stamp(1), "go test ./internal/payments/stripe", 0, "PASS"),
			}},
			wantKinds:   []string{"code_change_without_relevant_test"},
			wantCounter: map[string][]string{"code_change_without_relevant_test": {"child"}},
		},
		{
			name: "unrelated test does not verify a changed package",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("unrelated", stamp(1), "go test ./internal/auth", 0, "PASS"),
			}},
			wantKinds:      []string{"code_change_without_relevant_test"},
			wantConfidence: map[string]float64{"code_change_without_relevant_test": 0.70},
			wantSupporting: map[string][]string{"code_change_without_relevant_test": {"change"}},
			wantCounter:    map[string][]string{"code_change_without_relevant_test": {"unrelated"}},
		},
		{
			name: "test before a change is not verification",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				testEvent("before", stamp(0), "go test ./internal/payments", 0, "PASS"),
				writeEvent("change", stamp(1), "internal/payments/charge.go"),
			}},
			wantKinds:      []string{"code_change_without_relevant_test"},
			wantConfidence: map[string]float64{"code_change_without_relevant_test": 0.70},
			wantSupporting: map[string][]string{"code_change_without_relevant_test": {"change"}},
			wantCounter:    map[string][]string{"code_change_without_relevant_test": {"before"}},
		},
		{
			name: "delayed targeted test is lower confidence positive evidence",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("delayed", stamp(20), "go test ./internal/payments", 0, "PASS"),
			}},
			wantKinds:      []string{"verification_detected"},
			wantConfidence: map[string]float64{"verification_detected": 0.60},
			wantSupporting: map[string][]string{"verification_detected": {"change", "delayed"}},
		},
		{
			name: "pathless linked commit cannot create a missing test gap",
			input: Input{ProjectID: projectID, SessionID: sessionID, Commits: []db.SessionCommit{{
				ID: "commit-record", SessionID: sessionID, ProjectID: projectID, SHA: "abc123", CommitTS: stamp(0), Files: 2, Insertions: 12,
			}}},
		},
		{
			name: "pathless commit paired with excluded writes emits no missing test gap",
			input: Input{ProjectID: projectID, SessionID: sessionID, Commits: []db.SessionCommit{{
				ID: "commit-record", SessionID: sessionID, ProjectID: projectID, SHA: "abc123", CommitTS: stamp(0), Files: 1, Insertions: 3,
			}}, Events: []db.Event{
				writeEvent("lockfile", stamp(0), "pnpm-lock.yaml"),
			}},
		},
		{
			name: "docs only writes do not create an observation",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("docs", stamp(0), "README.md"),
				writeEvent("docs-directory", stamp(1), "docs/design.go"),
				boundaryEvent("stop", stamp(2)),
			}},
		},
		{
			name: "generated and vendor writes do not create an observation",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("generated", stamp(0), "web/generated/client.ts"),
				writeEvent("vendor", stamp(1), "vendor/github.com/acme/lib.go"),
				writeEvent("lockfile", stamp(2), "go.sum"),
			}},
		},
		{
			name: "whitespace-only edits do not create an observation",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				formattingEditEvent("format", stamp(0), "internal/payments/charge.go", "func Charge() {\nreturn nil\n}", "func Charge() {\n\treturn nil\n}"),
			}},
		},
		{
			name: "repeated unresolved failures after a change are high severity",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("failed", stamp(1), "go test ./internal/payments", 1, "FAIL\tforge/internal/payments"),
			}, Failures: []db.FailureSignature{{
				ID: "failure", ProjectID: projectID, SessionID: sessionID, CommandFamily: "go test", RepeatCount: 2, LastSeenTS: stamp(1), NormalizedMessage: "charge test failed",
			}}},
			wantKinds:      []string{"unresolved_failure_after_change"},
			wantConfidence: map[string]float64{"unresolved_failure_after_change": 0.90},
			wantSupporting: map[string][]string{"unresolved_failure_after_change": {"change", "failure"}},
		},
		{
			name: "a repeated failure after success is a regression with counter evidence",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("passed", stamp(1), "go test ./internal/payments", 0, "PASS"),
				testEvent("failed", stamp(2), "go test ./internal/payments", 1, "FAIL\tforge/internal/payments"),
			}, Failures: []db.FailureSignature{{
				ID: "failure", ProjectID: projectID, SessionID: sessionID, CommandFamily: "go test", RepeatCount: 3, LastSeenTS: stamp(2), NormalizedMessage: "charge test failed",
			}}},
			wantKinds:      []string{"verification_detected", "repeated_regression"},
			wantConfidence: map[string]float64{"verification_detected": 0.60, "repeated_regression": 0.95},
			wantSupporting: map[string][]string{
				"verification_detected": {"change", "passed"},
				"repeated_regression":   {"change", "failure"},
			},
			wantCounter: map[string][]string{"repeated_regression": {"passed"}},
		},
		{
			name: "unrelated repeated failure does not claim a causal observation",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("failed", stamp(1), "go test ./internal/auth", 1, "FAIL\tforge/internal/auth"),
			}, Failures: []db.FailureSignature{{
				ID: "failure", ProjectID: projectID, SessionID: sessionID, CommandFamily: "go test", RepeatCount: 2, LastSeenTS: stamp(1), NormalizedMessage: "auth test failed",
			}}},
			wantKinds:   []string{"code_change_without_relevant_test"},
			wantCounter: map[string][]string{"code_change_without_relevant_test": {"failed"}},
		},
		{
			name: "unrelated repeated failure does not claim a regression",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", stamp(0), "internal/payments/charge.go"),
				testEvent("passed", stamp(1), "go test ./internal/payments", 0, "PASS"),
				testEvent("failed", stamp(2), "go test ./internal/auth", 1, "FAIL\tforge/internal/auth"),
			}, Failures: []db.FailureSignature{{
				ID: "failure", ProjectID: projectID, SessionID: sessionID, CommandFamily: "go test", RepeatCount: 3, LastSeenTS: stamp(2), NormalizedMessage: "auth test failed",
			}}},
			wantKinds: []string{"verification_detected"},
		},
		{
			name: "a single session end without changes or tests is insufficient",
			input: Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				boundaryEvent("stop", stamp(0)),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectVerification(tt.input)
			assertDrafts(t, got, tt.wantKinds, tt.wantConfidence, tt.wantSupporting, tt.wantCounter)
		})
	}
}

func TestDetectVerificationRecognizesTestCommandEcosystems(t *testing.T) {
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	commands := []struct {
		name    string
		command string
	}{
		{"go", "go test ./..."},
		{"node", "pnpm test"},
		{"python", "python -m pytest"},
		{"rust", "cargo test"},
		{"java maven", "mvn test"},
		{"java gradle", "./gradlew test"},
		{"ruby", "bundle exec rspec"},
		{"dotnet", "dotnet test"},
	}

	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectVerification(Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", base.Format(time.RFC3339), "src/payments/charge.go"),
				testEvent("test", base.Add(time.Minute).Format(time.RFC3339), tt.command, 0, "PASS"),
			}})
			assertDrafts(t, got, []string{"verification_detected"}, nil, nil, nil)
		})
	}
}

func TestDetectVerificationFailedTestDoesNotCountAsVerification(t *testing.T) {
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	got := DetectVerification(Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
		writeEvent("change", base.Format(time.RFC3339), "internal/payments/charge.go"),
		testEvent("failed", base.Add(time.Minute).Format(time.RFC3339), "go test ./internal/payments", 1, "FAIL\tforge/internal/payments"),
	}})
	assertDrafts(t, got, []string{"code_change_without_relevant_test"}, nil, nil, map[string][]string{
		"code_change_without_relevant_test": {"failed"},
	})
}

func TestDetectVerificationParsesTextOnlyOutcomesConservatively(t *testing.T) {
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		output    string
		wantKinds []string
	}{
		{"zero failure summary is success", "Tests: 5 passed, 0 failures", []string{"verification_detected"}},
		{"ambiguous failure word is unknown", "Previous failures are documented above", []string{"code_change_without_relevant_test"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectVerification(Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
				writeEvent("change", base.Format(time.RFC3339), "internal/payments/charge.go"),
				textTestEvent("test", base.Add(time.Minute).Format(time.RFC3339), "go test ./internal/payments", tt.output),
			}})
			assertDrafts(t, got, tt.wantKinds, nil, nil, nil)
		})
	}
}

func TestDetectVerificationDraftsAreAuditable(t *testing.T) {
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	got := DetectVerification(Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
		writeEvent("change", base.Format(time.RFC3339), "internal/payments/charge.go"),
		testEvent("failed", base.Add(time.Minute).Format(time.RFC3339), "go test ./internal/payments", 1, "FAIL\tforge/internal/payments"),
	}, Failures: []db.FailureSignature{{
		ID: "failure", ProjectID: projectID, SessionID: sessionID, CommandFamily: "go test", RepeatCount: 2, LastSeenTS: base.Add(time.Minute).Format(time.RFC3339), NormalizedMessage: "charge test failed",
	}}})
	assertDrafts(t, got, []string{"unresolved_failure_after_change"}, nil, map[string][]string{
		"unresolved_failure_after_change": {"change", "failure"},
	}, nil)
	draft := got[0]
	if draft.Severity != "error" || draft.Summary == "" {
		t.Errorf("draft severity/summary = %q/%q, want error/non-empty", draft.Severity, draft.Summary)
	}
	assertSource(t, draft.SupportingSources[0], "event", "change", "internal/payments/charge.go")
	assertSource(t, draft.SupportingSources[1], "failure_signature", "failure", "charge test failed")

	regression := DetectVerification(Input{ProjectID: projectID, SessionID: sessionID, Events: []db.Event{
		writeEvent("change", base.Format(time.RFC3339), "internal/payments/charge.go"),
		testEvent("passed", base.Add(time.Minute).Format(time.RFC3339), "go test ./internal/payments", 0, "PASS"),
		testEvent("failed", base.Add(2*time.Minute).Format(time.RFC3339), "go test ./internal/payments", 1, "FAIL\tforge/internal/payments"),
	}, Failures: []db.FailureSignature{{
		ID: "failure", ProjectID: projectID, SessionID: sessionID, CommandFamily: "go test", RepeatCount: 3, LastSeenTS: base.Add(2 * time.Minute).Format(time.RFC3339), NormalizedMessage: "charge test failed",
	}}})
	var regressionDraft ObservationDraft
	for _, candidate := range regression {
		if candidate.Kind == "repeated_regression" {
			regressionDraft = candidate
			break
		}
	}
	if regressionDraft.Kind == "" {
		t.Fatalf("missing regression draft in %#v", regression)
	}
	assertSource(t, regressionDraft.CounterSources[0], "event", "passed", "go test ./internal/payments")
}

func writeEvent(id, ts, path string) db.Event {
	return db.Event{ID: id, TS: ts, ProjectID: projectID, SessionID: sessionID, EventType: "PostToolUse", ToolName: "Edit", Payload: `{"file_path":"` + path + `"}`}
}

func testEvent(id, ts, command string, exitCode int, output string) db.Event {
	payload := fmt.Sprintf(`{"tool_input":{"command":%q},"tool_response":{"exit_code":%d,"stdout":%q}}`, command, exitCode, output)
	return db.Event{ID: id, TS: ts, ProjectID: projectID, SessionID: sessionID, EventType: "PostToolUse", ToolName: "Bash", Payload: payload}
}

func textTestEvent(id, ts, command, output string) db.Event {
	payload := fmt.Sprintf(`{"tool_input":{"command":%q},"tool_response":{"stdout":%q}}`, command, output)
	return db.Event{ID: id, TS: ts, ProjectID: projectID, SessionID: sessionID, EventType: "PostToolUse", ToolName: "Bash", Payload: payload}
}

func boundaryEvent(id, ts string) db.Event {
	return db.Event{ID: id, TS: ts, ProjectID: projectID, SessionID: sessionID, EventType: "Stop"}
}

func formattingEditEvent(id, ts, path, oldText, newText string) db.Event {
	payload := fmt.Sprintf(`{"file_path":%q,"old_string":%q,"new_string":%q}`, path, oldText, newText)
	return db.Event{ID: id, TS: ts, ProjectID: projectID, SessionID: sessionID, EventType: "PostToolUse", ToolName: "Edit", Payload: payload}
}

func assertDrafts(t *testing.T, got []ObservationDraft, wantKinds []string, wantConfidence map[string]float64, wantSupporting, wantCounter map[string][]string) {
	t.Helper()
	if len(got) != len(wantKinds) {
		t.Fatalf("DetectVerification() returned %d drafts, want %d: %#v", len(got), len(wantKinds), got)
	}
	byKind := make(map[string]ObservationDraft, len(got))
	for _, draft := range got {
		if draft.SkillKey != "verification.pre_ship" {
			t.Errorf("draft skill key = %q, want verification.pre_ship", draft.SkillKey)
		}
		byKind[draft.Kind] = draft
	}
	for _, kind := range wantKinds {
		draft, ok := byKind[kind]
		if !ok {
			t.Errorf("missing %q draft in %#v", kind, got)
			continue
		}
		if want, ok := wantConfidence[kind]; ok && draft.Confidence != want {
			t.Errorf("%s confidence = %.2f, want %.2f", kind, draft.Confidence, want)
		}
		if want, ok := wantSupporting[kind]; ok {
			assertSourceIDs(t, kind+" supporting", draft.SupportingSources, want)
		}
		if want, ok := wantCounter[kind]; ok {
			assertSourceIDs(t, kind+" counter", draft.CounterSources, want)
		}
	}
}

func assertSourceIDs(t *testing.T, label string, sources []SourceReference, want []string) {
	t.Helper()
	if len(sources) != len(want) {
		t.Fatalf("%s source count = %d, want %d: %#v", label, len(sources), len(want), sources)
	}
	for i, source := range sources {
		if source.SourceID != want[i] {
			t.Errorf("%s source %d ID = %q, want %q", label, i, source.SourceID, want[i])
		}
	}
}

func assertSource(t *testing.T, got SourceReference, wantType, wantID, wantExcerpt string) {
	t.Helper()
	if got.SourceType != wantType || got.SourceID != wantID || got.Excerpt != wantExcerpt {
		t.Errorf("source = %#v, want type=%q id=%q excerpt=%q", got, wantType, wantID, wantExcerpt)
	}
}
