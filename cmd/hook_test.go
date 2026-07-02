package main

import (
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)



func TestBuildSessionRecallOutput_IncludesPromptMatchedLesson(t *testing.T) {
	out := buildSessionRecallOutput(
		"forgememory-cli",
		nil,
		nil,
		nil,
		nil,
		&promptRecallMatch{
			SourceType: "principle",
			ProjectID:  "other-repo",
			Narrative:  "Treat refused-connection startup errors as a retryable daemon recovery path.",
			Score:      2.1,
		},
		nil,
		"",
	)

	if !strings.Contains(out, "Prompt-matched principle from other-repo (high confidence): Treat refused-connection startup errors as a retryable daemon recovery path.") {
		t.Fatalf("missing prompt-matched lesson: %q", out)
	}
}

func TestBuildSessionRecallOutput_IncludesActiveFailureAlert(t *testing.T) {
	out := buildSessionRecallOutput(
		"forgememory-cli",
		nil,
		nil,
		[]db.Alert{{
			AlertType: "repeated_failure",
			Narrative: "Forge keeps seeing the same rustc failure signature while running cargo build: error[e0599] no method named serve found",
		}},
		nil,
		nil,
		nil,
		"",
	)

	if !strings.Contains(out, "Active repeated failure: Forge keeps seeing the same rustc failure signature while running cargo build: error[e0599] no method named serve found.") {
		t.Fatalf("missing active repeated failure alert: %q", out)
	}
}

func TestBuildSessionRecallOutput_IncludesCachedDocsInsight(t *testing.T) {
	out := buildSessionRecallOutput(
		"forgememory-cli",
		nil,
		nil,
		nil,
		[]db.ExternalContextSummary{{
			Source:      "context7",
			LibraryName: "rust",
			Narrative:   "Rust E0599 usually means the method is not in scope or the trait providing it is not imported",
		}},
		nil,
		nil,
		"",
	)

	if !strings.Contains(out, "Official docs hint from context7 rust: Rust E0599 usually means the method is not in scope or the trait providing it is not imported.") {
		t.Fatalf("missing cached docs insight: %q", out)
	}
}

func TestBuildSessionRecallOutput_PrefersOfficialDocsHintOverRepeatedFailureAlert(t *testing.T) {
	out := buildSessionRecallOutput(
		"forgememory-cli",
		nil,
		nil,
		[]db.Alert{{
			AlertType: "repeated_failure",
			Narrative: "Forge keeps seeing the same rustc failure signature while running cargo build: error[e0599] no method named serve found",
		}},
		[]db.ExternalContextSummary{{
			Source:      "context7",
			LibraryName: "rust",
			Narrative:   "Rust E0599 usually means the method is not in scope or the trait providing it is not imported.",
		}},
		nil,
		nil,
		"",
	)

	if !strings.Contains(out, "Official docs hint from context7 rust: Rust E0599 usually means the method is not in scope or the trait providing it is not imported.") {
		t.Fatalf("missing official docs hint: %q", out)
	}
	if strings.Contains(out, "Active repeated failure:") {
		t.Fatalf("expected repeated failure alert to be suppressed when official docs hint exists, got %q", out)
	}
}



func TestBuildSessionRecallOutput_LowConfidenceMatchDropped(t *testing.T) {
	out := buildSessionRecallOutput(
		"forgememory-cli",
		nil,
		nil,
		nil,
		nil,
		&promptRecallMatch{
			SourceType: "principle",
			ProjectID:  "other-repo",
			Narrative:  "Low confidence match should not appear.",
			Score:      1.3,
		},
		nil,
		"",
	)
	if strings.Contains(out, "Low confidence match") {
		t.Fatalf("expected low-confidence match to be dropped, got: %q", out)
	}
}

func TestLoadSessionRecallContext_FallsBackToGlobalRecentContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if _, err := database.InsertPrinciple(&db.Principle{
		ProjectID:   "forgememory-cli",
		Title:       "Cross-project fallback",
		Narrative:   "Use recent global context when project-specific recall is empty.",
		Type:        "pattern",
		ImpactScore: 0.6,
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if err := database.InsertSessionSummary(&db.SessionSummary{
		SessionID: "s1",
		ProjectID: "forgememory-cli",
		Learnings: "Recent summaries should still surface when project IDs do not match.",
	}); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}

	principles, summaries, alerts, externalSummaries, promptMatch, _ := loadSessionRecallContext(database, "proj", "")
	if len(principles) == 0 {
		t.Fatal("expected global principle fallback")
	}
	if len(summaries) == 0 {
		t.Fatal("expected global session summary fallback")
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts, got %#v", alerts)
	}
	if len(externalSummaries) != 0 {
		t.Fatalf("expected no external summaries, got %#v", externalSummaries)
	}
	if promptMatch != nil {
		t.Fatalf("expected no prompt match without prompt text, got %#v", promptMatch)
	}
	if principles[0].ProjectID != "forgememory-cli" {
		t.Fatalf("expected fallback principle project_id to be forgememory-cli, got %q", principles[0].ProjectID)
	}
	if summaries[0].ProjectID != "forgememory-cli" {
		t.Fatalf("expected fallback summary project_id to be forgememory-cli, got %q", summaries[0].ProjectID)
	}


}

func TestLoadSessionRecallContext_ReturnsFreshExternalSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := database.UpsertExternalContextSummary(&db.ExternalContextSummary{
		ProjectID:  "api-service",
		Source:     "context7",
		Query:      "rust e0599 cargo build",
		Title:      "Rust E0599",
		Narrative:  "E0599 means a method is missing or its trait is not in scope",
		TrustScore: 0.9,
		ExpiresAt:  time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpsertExternalContextSummary: %v", err)
	}

	_, _, _, externalSummaries, _, _ := loadSessionRecallContext(database, "api-service", "cargo build rust error e0599")
	if len(externalSummaries) != 1 {
		t.Fatalf("len(externalSummaries) = %d, want 1", len(externalSummaries))
	}
	if externalSummaries[0].Source != "context7" {
		t.Fatalf("Source = %q, want context7", externalSummaries[0].Source)
	}
}

func TestExtractPromptText_PrefersUserMessageContent(t *testing.T) {
	payload := `{
		"messages": [
			{"role":"system","content":"ignore this"},
			{"role":"user","content":[{"type":"text","text":"implement windows daemon retry path"}]}
		],
		"prompt":"secondary fallback"
	}`

	text := extractPromptText(payload)
	if !strings.Contains(text, "implement windows daemon retry path") {
		t.Fatalf("expected user content in extracted prompt, got %q", text)
	}
	if strings.Contains(text, "ignore this") {
		t.Fatalf("should not surface system content, got %q", text)
	}
}

func TestParseHookInput_UsesCommonGeminiFields(t *testing.T) {
	payload := `{
		"session_id":"sess-123",
		"cwd":"/tmp/project",
		"hook_event_name":"BeforeAgent",
		"tool_name":"run_shell_command",
		"prompt":"implement daemon retry"
	}`

	input, eventType := parseHookInput(payload, "unknown")
	if eventType != "BeforeAgent" {
		t.Fatalf("eventType = %q, want BeforeAgent", eventType)
	}
	if input.SessionID != "sess-123" {
		t.Fatalf("SessionID = %q, want sess-123", input.SessionID)
	}
	if input.CWD != "/tmp/project" {
		t.Fatalf("CWD = %q, want /tmp/project", input.CWD)
	}
	if input.ToolName != "run_shell_command" {
		t.Fatalf("ToolName = %q, want run_shell_command", input.ToolName)
	}
}

func TestFindBestPromptRecall_PrefersCrossProjectPrinciple(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	seed := []db.Principle{
		{
			ProjectID:     "payments-service",
			Title:         "Retry daemon handshake",
			Narrative:     "Treat refused connection during daemon startup as a retryable recovery path instead of a fatal error.",
			Type:          "bugfix",
			ImpactScore:   0.92,
			Concepts:      []string{"pattern", "gotcha"},
			FilesModified: []string{"cmd/startup.go"},
		},
		{
			ProjectID:     "forgememory-cli",
			Title:         "Local daemon rule",
			Narrative:     "Current-project lessons should not be returned as cross-project proactive matches.",
			Type:          "pattern",
			ImpactScore:   0.95,
			FilesModified: []string{"cmd/hook.go"},
		},
		{
			ProjectID:     "notes-app",
			Title:         "Theme polish",
			Narrative:     "Use bold typography for dashboard views.",
			Type:          "preference",
			ImpactScore:   0.4,
			FilesModified: []string{"ui/theme.css"},
		},
	}
	for _, principle := range seed {
		p := principle
		if _, err := database.InsertPrinciple(&p); err != nil {
			t.Fatalf("InsertPrinciple: %v", err)
		}
	}

	match := findBestPromptRecall(database, "forgememory-cli", "Implement daemon startup retry when the client sees refused connection on Windows.")
	if match == nil {
		t.Fatal("expected a prompt-matched recall")
	}
	if match.ProjectID != "payments-service" {
		t.Fatalf("expected payments-service match, got %q", match.ProjectID)
	}
	if match.SourceType != "principle" {
		t.Fatalf("expected principle source type, got %q", match.SourceType)
	}
	if len(match.MatchedTerms) < 2 {
		t.Fatalf("expected multiple matched terms, got %#v", match.MatchedTerms)
	}
}

func TestFindBestPromptRecall_CanUseSessionSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := database.InsertSessionSummary(&db.SessionSummary{
		SessionID:     "sess-1",
		ProjectID:     "design-system",
		Request:       "Add startup retry when daemon socket is not ready yet.",
		Investigation: "Initial startup fails with refused connection before the pipe comes up.",
		Learnings:     "Retry refused connection for a short window during startup instead of failing immediately.",
	}); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}

	match := findBestPromptRecall(database, "forgememory-cli", "Implement startup retry for refused connection while the daemon socket is still coming up.")
	if match == nil {
		t.Fatal("expected session-summary based prompt recall")
	}
	if match.SourceType != "session lesson" {
		t.Fatalf("expected session lesson source type, got %q", match.SourceType)
	}
	if match.ProjectID != "design-system" {
		t.Fatalf("expected design-system project, got %q", match.ProjectID)
	}
}

func TestFindBestPromptRecall_ReturnsSameProjectHighConfidenceMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if _, err := database.InsertPrinciple(&db.Principle{
		ProjectID:     "forgememory-cli",
		Title:         "Retry daemon startup",
		Narrative:     "Treat refused connection during startup as a retryable recovery path.",
		Type:          "bugfix",
		ImpactScore:   0.95,
		FilesModified: []string{"cmd/startup.go"},
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	// Same-project principles are now included in recall — high-confidence match should surface.
	match := findBestPromptRecall(database, "forgememory-cli", "Implement daemon startup retry for refused connection.")
	if match == nil {
		t.Fatal("expected same-project high-confidence match to be returned")
	}
	if match.ProjectID != "forgememory-cli" {
		t.Fatalf("ProjectID = %q, want forgememory-cli", match.ProjectID)
	}
}

func TestFindBestPromptRecall_NeverInjectsConflictingPrinciples(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Insert a conflicting principle in the same project.
	pA := &db.Principle{
		ID: "conflict-a", ProjectID: "forgememory-cli",
		Title: "Conflicting principle A", Narrative: "Always use daemon startup retry for refused connection errors.",
		Type: "pattern", ImpactScore: 0.9,
	}
	pB := &db.Principle{
		ID: "conflict-b", ProjectID: "other-project",
		Title: "Conflicting principle B", Narrative: "Never retry daemon startup on refused connection — fail fast instead.",
		Type: "pattern", ImpactScore: 0.9,
	}
	if _, err := database.InsertPrinciple(pA); err != nil {
		t.Fatalf("InsertPrinciple A: %v", err)
	}
	if _, err := database.InsertPrinciple(pB); err != nil {
		t.Fatalf("InsertPrinciple B: %v", err)
	}
	if err := database.MarkConflicting(pA.ID, pB.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	// Neither same-project nor cross-project scan should surface conflicting principles.
	match := findBestPromptRecall(database, "forgememory-cli", "daemon startup retry refused connection error")
	if match != nil {
		t.Fatalf("expected no match — conflicting principles must not be injected, got: %+v", match)
	}
}

func TestLoadSessionRecallContext_NeverInjectsConflictingPrincipleAsFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	pA := &db.Principle{
		ID: "fallback-a", ProjectID: "proj-a",
		Title: "Conflicting fallback A", Narrative: "Use X approach for auth tokens.",
		Type: "pattern", ImpactScore: 0.8,
	}
	pB := &db.Principle{
		ID: "fallback-b", ProjectID: "proj-b",
		Title: "Conflicting fallback B", Narrative: "Use Y approach for auth tokens.",
		Type: "pattern", ImpactScore: 0.8,
	}
	if _, err := database.InsertPrinciple(pA); err != nil {
		t.Fatalf("InsertPrinciple A: %v", err)
	}
	if _, err := database.InsertPrinciple(pB); err != nil {
		t.Fatalf("InsertPrinciple B: %v", err)
	}
	if err := database.MarkConflicting(pA.ID, pB.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	// Request with empty project falls back to global recent — must exclude conflicting.
	principles, _, _, _, _, _ := loadSessionRecallContext(database, "unknown-project", "")
	for _, p := range principles {
		if p.Status == "conflicting" {
			t.Fatalf("conflicting principle must not be injected as fallback context: %+v", p)
		}
	}
}

func TestFindBestPromptRecall_PrefersMoreRecentMatchOnScoreTie(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	oldTS := time.Now().Add(-45 * 24 * time.Hour).UTC().Format(time.RFC3339)
	newTS := time.Now().Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339)

	for _, principle := range []db.Principle{
		{
			TS:            oldTS,
			ProjectID:     "legacy-service",
			Title:         "Retry daemon startup",
			Narrative:     "Retry refused connection during daemon startup instead of failing immediately.",
			Type:          "bugfix",
			ImpactScore:   0.9,
			FilesModified: []string{"cmd/startup.go"},
		},
		{
			TS:            newTS,
			ProjectID:     "payments-service",
			Title:         "Retry daemon startup",
			Narrative:     "Retry refused connection during daemon startup instead of failing immediately.",
			Type:          "bugfix",
			ImpactScore:   0.9,
			FilesModified: []string{"cmd/startup.go"},
		},
	} {
		p := principle
		if _, err := database.InsertPrinciple(&p); err != nil {
			t.Fatalf("InsertPrinciple: %v", err)
		}
	}

	match := findBestPromptRecall(database, "forgememory-cli", "Implement daemon startup retry for refused connection.")
	if match == nil {
		t.Fatal("expected a prompt-matched recall")
	}
	if match.ProjectID != "payments-service" {
		t.Fatalf("expected newer match to win tie-break, got %q", match.ProjectID)
	}
}

func TestFindBestPromptRecall_AllowsSingleLongTokenMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if _, err := database.InsertPrinciple(&db.Principle{
		TS:            time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
		ProjectID:     "design-system",
		Title:         "Dashboard shell loading",
		Narrative:     "Use a dashboard skeleton during startup to avoid layout jank.",
		Type:          "pattern",
		ImpactScore:   0.92,
		FilesModified: []string{"ui/dashboard.tsx"},
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	match := findBestPromptRecall(database, "forgememory-cli", "dashboard redesign")
	if match == nil {
		t.Fatal("expected current algorithm to allow a single long-token proactive match")
	}
	if match.ProjectID != "design-system" {
		t.Fatalf("expected design-system match, got %q", match.ProjectID)
	}
	if len(match.MatchedTerms) != 1 || match.MatchedTerms[0] != "dashboard" {
		t.Fatalf("expected only dashboard to match, got %#v", match.MatchedTerms)
	}
}

func TestIsNoisePayload_DockerBuild(t *testing.T) {
	noise := `2026-04-29T18:21:07.339710746Z #14 [builder 4/4] RUN cd payment && go build -o /payment-server .
2026-04-29T18:21:07.339715646Z #14 0.073 /bin/sh: cd: line 0: can't cd to payment: No such file or directory
#14 ERROR: process "/bin/sh -c cd payment && go build" did not complete successfully: exit code: 2
Dockerfile:6
error: failed to solve: process did not complete successfully
extracting sha256:54bf7053e2d96c2c7f4637ad7580bd64345b3c9fabb163e1fdb8894aea8a9af0 5.9s done
sha256:54bf7053e2d96c2c7f4637ad7580bd64345b3c9fabb163e1fdb8894aea8a9af0 67.01MB / 67.01MB 0.7s done`
	if !isNoisePayload(noise) {
		t.Error("Docker build output should be detected as noise")
	}
}

func TestIsNoisePayload_CleanCode(t *testing.T) {
	clean := `{"tool_name":"Bash","tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok  github.com/forge/forge/cmd","exit_code":0}}`
	if isNoisePayload(clean) {
		t.Error("clean code output should NOT be detected as noise")
	}
}

func TestIsNoisePayload_ShortPayload(t *testing.T) {
	if isNoisePayload("sha256:abc123") {
		t.Error("short payload should not match noise patterns (len < 100)")
	}
}

func TestIsNoisePayload_GitHubActionsCI(t *testing.T) {
	ci := `##[error]Process completed with exit code 1.
::error::Something went wrong.
##[warning]This action is deprecated`
	if !isNoisePayload(ci) {
		t.Error("GitHub Actions CI output should be detected as noise")
	}
}

func TestIsNoisePayload_Empty(t *testing.T) {
	if isNoisePayload("") {
		t.Error("empty payload should not be noise")
	}
}
