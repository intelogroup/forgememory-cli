package db

import (
	"path/filepath"
	"testing"
	"time"
)

func openAlertsDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUpsertFailureSignature_Insert(t *testing.T) {
	db := openAlertsDB(t)
	sig := &FailureSignature{
		ProjectID:         "proj",
		SessionID:         "sess",
		SourceTool:        "claude",
		ToolName:          "Bash",
		Fingerprint:       "fp1",
		ErrorKind:         "build",
		NormalizedMessage: "build failed",
	}
	got, err := db.UpsertFailureSignature(sig)
	if err != nil {
		t.Fatalf("UpsertFailureSignature: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.RepeatCount != 1 {
		t.Errorf("RepeatCount = %d, want 1", got.RepeatCount)
	}
	if got.ID == "" {
		t.Error("expected ID to be set")
	}
}

func TestUpsertFailureSignature_Increment(t *testing.T) {
	db := openAlertsDB(t)
	sig := &FailureSignature{
		ProjectID:         "proj",
		SessionID:         "sess",
		SourceTool:        "claude",
		ToolName:          "Bash",
		Fingerprint:       "fp-inc",
		ErrorKind:         "build",
		NormalizedMessage: "build failed",
	}
	_, err := db.UpsertFailureSignature(sig)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	got, err := db.UpsertFailureSignature(sig)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result on second upsert")
	}
	if got.RepeatCount != 2 {
		t.Errorf("RepeatCount = %d, want 2", got.RepeatCount)
	}
}

func TestFailureSignaturesByProjectLimitedOrdersAndCapsHistory(t *testing.T) {
	database := openAlertsDB(t)
	for i, sessionID := range []string{"old", "middle", "new"} {
		at := time.Date(2026, time.August, 13, 12+i, 0, 0, 0, time.UTC)
		for n := 0; n < 2; n++ {
			if _, err := database.UpsertFailureSignature(&FailureSignature{
				ProjectID: "proj", SessionID: sessionID, ToolName: "Bash", CommandFamily: "go test",
				Fingerprint: "limited-" + sessionID, ErrorKind: "test_failure", NormalizedMessage: "test failed",
				FirstSeenTS: at.Format(time.RFC3339), LastSeenTS: at.Format(time.RFC3339),
			}); err != nil {
				t.Fatalf("UpsertFailureSignature %s: %v", sessionID, err)
			}
		}
	}
	if _, err := database.UpsertFailureSignature(&FailureSignature{
		ProjectID: "proj", SessionID: "single", ToolName: "Bash", CommandFamily: "go test",
		Fingerprint: "limited-single", ErrorKind: "test_failure", NormalizedMessage: "test failed",
		FirstSeenTS: "2026-08-13T16:00:00Z", LastSeenTS: "2026-08-13T16:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertFailureSignature single: %v", err)
	}
	if _, err := database.UpsertFailureSignature(&FailureSignature{
		ProjectID: "proj", SessionID: "resolved", ToolName: "Bash", CommandFamily: "go test",
		Fingerprint: "limited-resolved", ErrorKind: "test_failure", NormalizedMessage: "test failed",
		FirstSeenTS: "2026-08-13T17:00:00Z", LastSeenTS: "2026-08-13T17:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertFailureSignature resolved: %v", err)
	}
	if _, err := database.ResolveFailureSignatures("proj", "resolved", "Bash", "", "2026-08-13T18:00:00Z"); err != nil {
		t.Fatalf("ResolveFailureSignatures: %v", err)
	}

	sigs, err := database.FailureSignaturesByProjectLimited("proj", 2)
	if err != nil {
		t.Fatalf("FailureSignaturesByProjectLimited: %v", err)
	}
	if len(sigs) != 2 || sigs[0].SessionID != "new" || sigs[1].SessionID != "middle" {
		t.Fatalf("limited failure signatures = %#v, want newest two repeated signatures", sigs)
	}
}

func TestFailureSignaturesByProjectPreservesAllHistory(t *testing.T) {
	database := openAlertsDB(t)
	for _, sig := range []FailureSignature{
		{ProjectID: "proj", SessionID: "one-off", ToolName: "Bash", Fingerprint: "one-off", RepeatCount: 1},
		{ProjectID: "proj", SessionID: "resolved", ToolName: "Bash", Fingerprint: "resolved", RepeatCount: 2, ResolvedTS: "2026-08-13T18:00:00Z"},
		{ProjectID: "proj", SessionID: "active", ToolName: "Bash", Fingerprint: "active", RepeatCount: 2},
	} {
		if _, err := database.UpsertFailureSignature(&sig); err != nil {
			t.Fatalf("UpsertFailureSignature %s: %v", sig.SessionID, err)
		}
	}

	all, err := database.FailureSignaturesByProject("proj")
	if err != nil {
		t.Fatalf("FailureSignaturesByProject: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all failure signatures = %#v, want one-off, resolved, and active rows", all)
	}

	limited, err := database.FailureSignaturesByProjectLimited("proj", 10)
	if err != nil {
		t.Fatalf("FailureSignaturesByProjectLimited: %v", err)
	}
	if len(limited) != 1 || limited[0].SessionID != "active" {
		t.Fatalf("limited failure signatures = %#v, want only active repeated row", limited)
	}
}

func TestUpsertActiveAlert_And_ActiveAlertsByProject(t *testing.T) {
	db := openAlertsDB(t)
	alert := &Alert{
		ProjectID:   "proj-a",
		SessionID:   "sess-a",
		AlertType:   "repeated_failure",
		Severity:    "high",
		Title:       "Build keeps failing",
		Narrative:   "cargo build errors repeating",
		Fingerprint: "alert-fp1",
		Score:       0.9,
		ExpiresAt:   time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if err := db.UpsertActiveAlert(alert); err != nil {
		t.Fatalf("UpsertActiveAlert: %v", err)
	}
	alerts, err := db.ActiveAlertsByProject("proj-a", 5)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(alerts))
	}
	if alerts[0].Title != "Build keeps failing" {
		t.Errorf("Title = %q, want 'Build keeps failing'", alerts[0].Title)
	}
}

func TestActiveAlertsByProject_EmptyProjectReturnsAll(t *testing.T) {
	db := openAlertsDB(t)
	for _, proj := range []string{"proj-x", "proj-y"} {
		if err := db.UpsertActiveAlert(&Alert{
			ProjectID:   proj,
			SessionID:   "sess",
			AlertType:   "repeated_failure",
			Severity:    "low",
			Title:       "alert-" + proj,
			Fingerprint: "fp-" + proj,
			Score:       0.5,
			ExpiresAt:   time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("UpsertActiveAlert: %v", err)
		}
	}
	alerts, err := db.ActiveAlertsByProject("", 10)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject(empty): %v", err)
	}
	if len(alerts) < 2 {
		t.Errorf("expected >= 2 alerts, got %d", len(alerts))
	}
}

func TestResolveFailureSignatures(t *testing.T) {
	db := openAlertsDB(t)
	sig := &FailureSignature{
		ProjectID:         "proj",
		SessionID:         "sess",
		SourceTool:        "claude",
		ToolName:          "Bash",
		Fingerprint:       "fp-resolve",
		ErrorKind:         "build",
		NormalizedMessage: "build failed",
	}
	if _, err := db.UpsertFailureSignature(sig); err != nil {
		t.Fatalf("UpsertFailureSignature: %v", err)
	}
	resolvedTS := time.Now().UTC().Format(time.RFC3339)
	ids, err := db.ResolveFailureSignatures("proj", "sess", "Bash", "", resolvedTS)
	if err != nil {
		t.Fatalf("ResolveFailureSignatures: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("resolved ids = %d, want 1", len(ids))
	}
	// After resolve, same fingerprint should no longer be active
	got, err := db.getActiveFailureSignature("proj", "sess", "fp-resolve")
	if err != nil {
		t.Fatalf("getActiveFailureSignature: %v", err)
	}
	if got != nil {
		t.Error("expected nil after resolve, got non-nil")
	}
}

func TestResolveFailureSignatures_EmptyArgs(t *testing.T) {
	db := openAlertsDB(t)
	ids, err := db.ResolveFailureSignatures("", "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil ids for empty args, got %v", ids)
	}
}

func TestAcknowledgeAlerts(t *testing.T) {
	db := openAlertsDB(t)
	alert := &Alert{
		ProjectID:   "proj",
		SessionID:   "sess",
		AlertType:   "repeated_failure",
		Severity:    "high",
		Title:       "to ack",
		Fingerprint: "fp-ack",
		Score:       0.8,
		SourceRef:   "sig-id-1",
		ExpiresAt:   time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if err := db.UpsertActiveAlert(alert); err != nil {
		t.Fatalf("UpsertActiveAlert: %v", err)
	}
	resolvedTS := time.Now().UTC().Format(time.RFC3339)
	if err := db.AcknowledgeAlerts("proj", "sess", "repeated_failure", resolvedTS, []string{"sig-id-1"}); err != nil {
		t.Fatalf("AcknowledgeAlerts: %v", err)
	}
	// After ack, active alerts should be empty
	alerts, err := db.ActiveAlertsByProject("proj", 10)
	if err != nil {
		t.Fatalf("ActiveAlertsByProject: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 active alerts after ack, got %d", len(alerts))
	}
}

func TestAcknowledgeAlerts_EmptySourceRefs(t *testing.T) {
	db := openAlertsDB(t)
	// Should be a no-op
	if err := db.AcknowledgeAlerts("proj", "sess", "type", "ts", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
