package main

import (
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestBuildSessionRecallOutput_ProjectSummaryAndPrinciple(t *testing.T) {
	summaries := []db.SessionSummary{
		{ProjectID: "forgememory-cli", Learnings: "Codex was only capturing startup and stop hooks.", NextSteps: "Add PostToolUse for Codex."},
		{ProjectID: "forgememory-cli", Summary: "Prompt recall should be scoped to the current repo."},
	}
	principles := []db.Principle{
		{ProjectID: "forgememory-cli", Narrative: "Normalize project IDs to the repo basename before storing events and principles."},
	}

	out := buildSessionRecallOutput("forgememory-cli", summaries, principles, nil, nil, nil)

	if !strings.Contains(out, "## Forge Context") {
		t.Fatalf("missing recall heading: %q", out)
	}
	if !strings.Contains(out, "Recent lessons for forgememory-cli") {
		t.Fatalf("missing project-scoped summary: %q", out)
	}
	if !strings.Contains(out, "Active principle: Normalize project IDs to the repo basename before storing events and principles.") {
		t.Fatalf("missing active principle sentence: %q", out)
	}
	if strings.Contains(out, "\n-") {
		t.Fatalf("startup context should be compact prose, got: %q", out)
	}
}

func TestBuildSessionRecallOutput_NextStepFallback(t *testing.T) {
	summaries := []db.SessionSummary{
		{ProjectID: "forgememory-cli", Learnings: "Session synthesis is landing successfully.", NextSteps: "Verify the next Codex session writes summaries."},
	}

	out := buildSessionRecallOutput("forgememory-cli", summaries, nil, nil, nil, nil)

	if !strings.Contains(out, "Next step: Verify the next Codex session writes summaries.") {
		t.Fatalf("expected next-step fallback, got: %q", out)
	}
}

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
	)

	if !strings.Contains(out, "Official docs hint from context7 rust: Rust E0599 usually means the method is not in scope or the trait providing it is not imported.") {
		t.Fatalf("missing official docs hint: %q", out)
	}
	if strings.Contains(out, "Active repeated failure:") {
		t.Fatalf("expected repeated failure alert to be suppressed when official docs hint exists, got %q", out)
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

	principles, summaries, alerts, externalSummaries, promptMatch := loadSessionRecallContext(database, "proj", "")
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

	out := buildSessionRecallOutput("proj", summaries, principles, nil, nil, nil)
	if !strings.Contains(out, "Recent lessons for proj") {
		t.Fatalf("expected output to stay scoped to requested project id, got %q", out)
	}
	if !strings.Contains(out, "Active principle: Use recent global context when project-specific recall is empty.") {
		t.Fatalf("expected fallback principle narrative in output, got %q", out)
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

	_, _, _, externalSummaries, _ := loadSessionRecallContext(database, "api-service", "cargo build rust error e0599")
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

func TestFindBestPromptRecall_SkipsSameProjectMatches(t *testing.T) {
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

	match := findBestPromptRecall(database, "forgememory-cli", "Implement daemon startup retry for refused connection.")
	if match != nil {
		t.Fatalf("expected no proactive cross-project recall for same-project memory, got %#v", match)
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
