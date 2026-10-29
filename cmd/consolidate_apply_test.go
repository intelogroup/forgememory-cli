package main

import (
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/db"
)

func openApplyTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func applyBatch(t *testing.T, database *db.DB, results ...ConsolidationResult) error {
	t.Helper()
	ctx := &consolidationCtx{
		database: database,
		batches:  []*consolidationBatch{{id: "test-batch", results: results}},
	}
	return ctx.applyResults()
}

func validateOn(t *testing.T, database *db.DB, batch []db.Principle, results []ConsolidationResult) ([]ConsolidationResult, int) {
	t.Helper()
	ctx := &consolidationCtx{database: database}
	valid, rejected, err := ctx.validateResults(batch, results)
	if err != nil {
		t.Fatalf("validateResults: %v", err)
	}
	return valid, rejected
}

func memoryOpFor(t *testing.T, database *db.DB, targetID string) string {
	t.Helper()
	var op string
	if err := database.Conn().QueryRow(
		`SELECT op FROM memory_ops WHERE target_id = ? ORDER BY ts DESC LIMIT 1`, targetID,
	).Scan(&op); err != nil {
		t.Fatalf("read memory_ops for %s: %v", targetID, err)
	}
	return op
}

// TestApplyResultsUpdateKeepsRowReadable pins the end-to-end apply path: the
// consolidation UPDATE must go through the signing chokepoint, or the row is
// dropped by GetPrincipleByID (which scanPrinciples filters) right after the
// run.
func TestApplyResultsUpdateKeepsRowReadable(t *testing.T) {
	database := openApplyTestDB(t)

	p := &db.Principle{Title: "t", Narrative: "n", ProjectID: "proj", Type: "pattern", ImpactScore: 0.4}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}

	err = applyBatch(t, database, ConsolidationResult{
		Operation:   "UPDATE",
		OriginalID:  p.ID,
		NewID:       p.ID,
		Title:       "t2",
		Narrative:   "n2",
		ImpactScore: 0.8,
		Reason:      "test",
	})
	if err != nil {
		t.Fatalf("applyResults: %v", err)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if got == nil {
		t.Fatal("updated principle is unreadable — applyResults bypassed signing")
	}
	if got.Title != "t2" || got.Narrative != "n2" || got.ImpactScore != 0.8 {
		t.Errorf("row not updated: %+v", got)
	}
	if op := memoryOpFor(t, database, p.ID); op != "UPDATE" {
		t.Errorf("audit op = %q, want UPDATE", op)
	}
}

func TestApplyResultsDeleteExpiresInsteadOfDeleting(t *testing.T) {
	database := openApplyTestDB(t)

	p := &db.Principle{Title: "t", Narrative: "n", ProjectID: "proj", Type: "pattern"}
	if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}

	if err := applyBatch(t, database, ConsolidationResult{
		Operation:  "DELETE",
		OriginalID: p.ID,
		Reason:     "contradicted",
	}); err != nil {
		t.Fatalf("applyResults: %v", err)
	}

	var status, expiredAt string
	if err := database.Conn().QueryRow(
		`SELECT COALESCE(status,''), COALESCE(expired_at,'') FROM principles WHERE id = ?`, p.ID,
	).Scan(&status, &expiredAt); err != nil {
		t.Fatalf("row deleted instead of expired: %v", err)
	}
	if status != "revoked" || expiredAt == "" {
		t.Errorf("status=%q expired_at=%q, want revoked with expired_at set", status, expiredAt)
	}
	if op := memoryOpFor(t, database, p.ID); op != "EXPIRE" {
		t.Errorf("audit op = %q, want EXPIRE", op)
	}
}

// TestApplyResultsAddRevivesExpiredRow covers ADD-after-EXPIRE: the table has
// UNIQUE(fingerprint), so the insert is ignored and the expired row is
// revived with the new text instead of being lost.
func TestApplyResultsAddRevivesExpiredRow(t *testing.T) {
	database := openApplyTestDB(t)

	p := &db.Principle{Title: "same", Narrative: "v1", ProjectID: "proj", Type: "pattern"}
	if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}
	if err := database.ExpirePrinciple(p.ID, "2026-01-02T00:00:00Z", ""); err != nil {
		t.Fatalf("ExpirePrinciple: %v", err)
	}

	if err := applyBatch(t, database, ConsolidationResult{
		Operation:   "ADD",
		NewID:       "replacement-id",
		Title:       "same",
		Narrative:   "v2",
		ProjectID:   "proj",
		Type:        "pattern",
		ImpactScore: 0.9,
		Reason:      "test",
	}); err != nil {
		t.Fatalf("applyResults: %v", err)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("revived row unreadable: got=%v err=%v", got, err)
	}
	if got.Narrative != "v2" {
		t.Errorf("narrative = %q, want v2", got.Narrative)
	}
	if op := memoryOpFor(t, database, p.ID); op != "REVIVE" {
		t.Errorf("audit op = %q, want REVIVE", op)
	}
}

// TestApplyResultsAddDuplicateActiveIsSkippedAndAudited guards against the
// old behavior, where INSERT OR IGNORE dropped the decision while the audit
// still claimed an ADD.
func TestApplyResultsAddDuplicateActiveIsSkippedAndAudited(t *testing.T) {
	database := openApplyTestDB(t)

	p := &db.Principle{Title: "same", Narrative: "keep me", ProjectID: "proj", Type: "pattern"}
	if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}

	if err := applyBatch(t, database, ConsolidationResult{
		Operation: "ADD",
		NewID:     "duplicate-id",
		Title:     "same",
		Narrative: "should not overwrite",
		ProjectID: "proj",
		Type:      "pattern",
		Reason:    "test",
	}); err != nil {
		t.Fatalf("applyResults: %v", err)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID: got=%v err=%v", got, err)
	}
	if got.Narrative != "keep me" {
		t.Errorf("active duplicate was overwritten: %q", got.Narrative)
	}
	if op := memoryOpFor(t, database, p.ID); op != "SKIP" {
		t.Errorf("audit op = %q, want SKIP", op)
	}
	var addCount int
	if err := database.Conn().QueryRow(
		`SELECT COUNT(*) FROM memory_ops WHERE op = 'ADD'`,
	).Scan(&addCount); err != nil {
		t.Fatalf("count ADD ops: %v", err)
	}
	if addCount != 0 {
		t.Errorf("ADD audit rows = %d, want 0", addCount)
	}
}

// TestApplyResultsRollsBackFailedBatch proves a batch is all-or-nothing: the
// first ADD succeeds inside the transaction, the second decision fails, and
// neither the row nor its audit entry survives.
func TestApplyResultsRollsBackFailedBatch(t *testing.T) {
	database := openApplyTestDB(t)

	err := applyBatch(t, database,
		ConsolidationResult{
			Operation:   "ADD",
			NewID:       "add-1",
			Title:       "new lesson",
			Narrative:   "body",
			ProjectID:   "proj",
			Type:        "pattern",
			ImpactScore: 0.7,
			Reason:      "test",
		},
		ConsolidationResult{
			Operation: "UPDATE",
			NewID:     "missing-id",
			Title:     "t",
			Narrative: "n",
			Reason:    "test",
		},
	)
	if err == nil {
		t.Fatal("expected the batch to fail")
	}

	var principles, ops int
	if err := database.Conn().QueryRow(
		`SELECT COUNT(*) FROM principles WHERE id = 'add-1'`,
	).Scan(&principles); err != nil {
		t.Fatalf("count principles: %v", err)
	}
	if err := database.Conn().QueryRow(`SELECT COUNT(*) FROM memory_ops`).Scan(&ops); err != nil {
		t.Fatalf("count memory_ops: %v", err)
	}
	if principles != 0 {
		t.Errorf("ADD from failed batch was committed (%d rows)", principles)
	}
	if ops != 0 {
		t.Errorf("audit rows from failed batch were committed (%d rows)", ops)
	}
}

func TestValidateResultsRejectsOutOfBatchTargets(t *testing.T) {
	database := openApplyTestDB(t)

	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{
		{Operation: "UPDATE", NewID: "p2", Title: "t", Narrative: "n", Reason: "r", Evidence: []string{"p1"}},
		{Operation: "DELETE", OriginalID: "p2", Reason: "r", Evidence: []string{"p1"}},
		{Operation: "ADD", NewID: "a1", Title: "t", Narrative: "n", ProjectID: "proj", Reason: "r", Evidence: []string{"p1"}}, // missing type
		{Operation: "MERGE", NewID: "p1", Reason: "r", Evidence: []string{"p1"}},
		{Operation: "DELETE", OriginalID: "p1", Reason: "r", Evidence: []string{"p1"}},
	})

	if len(valid) != 1 || valid[0].Operation != "DELETE" || valid[0].OriginalID != "p1" {
		t.Fatalf("valid = %+v, want only the in-batch DELETE", valid)
	}
	if rejected != 4 {
		t.Errorf("rejected = %d, want 4", rejected)
	}
}

func TestValidateResultsRequiresEvidenceAndReason(t *testing.T) {
	database := openApplyTestDB(t)

	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{
		{Operation: "UPDATE", NewID: "p1", Title: "t", Narrative: "n", Reason: "r"},                         // no evidence
		{Operation: "UPDATE", NewID: "p1", Title: "t", Narrative: "n", Evidence: []string{"p1"}},            // no reason
		{Operation: "UPDATE", NewID: "p1", Title: "t", Narrative: "n", Reason: "r", Evidence: []string{""}}, // blank evidence
	})
	if len(valid) != 0 || rejected != 3 {
		t.Fatalf("valid=%d rejected=%d, want 0/3", len(valid), rejected)
	}
}

func TestValidateResultsNormalizesAndClamps(t *testing.T) {
	database := openApplyTestDB(t)

	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{
		{
			Operation:   " update ",
			OriginalID:  "p1",
			Title:       "  t  ",
			Narrative:   "  n  ",
			Reason:      "  r  ",
			ImpactScore: 9.5,
			Evidence:    []string{" p1 ", ""},
		},
	})
	if rejected != 0 || len(valid) != 1 {
		t.Fatalf("valid=%d rejected=%d, want 1/0", len(valid), rejected)
	}
	got := valid[0]
	if got.Operation != "UPDATE" || got.NewID != "p1" {
		t.Errorf("operation/new_id not normalized: %+v", got)
	}
	if got.ImpactScore != 1.0 {
		t.Errorf("impact = %v, want clamped to 1", got.ImpactScore)
	}
	if got.Reason != "r" || got.Title != "t" || got.Narrative != "n" {
		t.Errorf("fields not trimmed: %+v", got)
	}
	if len(got.Evidence) != 1 || got.Evidence[0] != "p1" {
		t.Errorf("evidence not cleaned: %v", got.Evidence)
	}
}

// TestValidateResultsAnchorsUpdateToOriginalID: a model that emits a fresh
// new_id for an UPDATE still has the decision applied to the original row,
// instead of being rejected or retargeted.
func TestValidateResultsAnchorsUpdateToOriginalID(t *testing.T) {
	database := openApplyTestDB(t)

	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{{
		Operation:  "UPDATE",
		OriginalID: "p1",
		NewID:      "model-generated-uuid",
		Title:      "t",
		Narrative:  "n",
		Reason:     "r",
		Evidence:   []string{"p1"},
	}})
	if rejected != 0 || len(valid) != 1 {
		t.Fatalf("valid=%d rejected=%d, want 1/0", len(valid), rejected)
	}
	if valid[0].NewID != "p1" || valid[0].OriginalID != "p1" {
		t.Errorf("update not anchored to the original row: %+v", valid[0])
	}
}

// TestValidateResultsRejectsFabricatedEvidence is the point of resolution:
// a model that invents an ID (the old prompt literally suggested "evt-999")
// must not get a decision persisted.
func TestValidateResultsRejectsFabricatedEvidence(t *testing.T) {
	database := openApplyTestDB(t)
	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{{
		Operation:  "UPDATE",
		OriginalID: "p1",
		Title:      "t",
		Narrative:  "n",
		Reason:     "r",
		Evidence:   []string{"evt-999", "session-that-never-existed"},
	}})
	if len(valid) != 0 || rejected != 1 {
		t.Fatalf("valid=%d rejected=%d, want 0/1", len(valid), rejected)
	}
}

// TestValidateResultsAcceptsStoredEventEvidence: a real event ID from the
// store satisfies evidence, even when the event is not part of the batch.
func TestValidateResultsAcceptsStoredEventEvidence(t *testing.T) {
	database := openApplyTestDB(t)
	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	ev := &db.Event{
		ID:         "evt-real-1",
		SessionID:  "sess-1",
		ProjectID:  "proj",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    `{}`,
	}
	if err := database.InsertEvent(ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{{
		Operation:  "UPDATE",
		OriginalID: "p1",
		Title:      "t",
		Narrative:  "n",
		Reason:     "r",
		Evidence:   []string{ev.ID},
	}})
	if len(valid) != 1 || rejected != 0 {
		t.Fatalf("valid=%d rejected=%d, want 1/0", len(valid), rejected)
	}
}

// TestValidateResultsAcceptsStoredSessionEvidence covers both ways a session
// summary can be cited: its row id and its session_id.
func TestValidateResultsAcceptsStoredSessionEvidence(t *testing.T) {
	database := openApplyTestDB(t)
	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	summary := &db.SessionSummary{SessionID: "sess-real-1", ProjectID: "proj", Summary: "did work"}
	if err := database.InsertSessionSummary(summary); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}

	for _, evidenceID := range []string{summary.ID, summary.SessionID} {
		valid, rejected := validateOn(t, database, batch, []ConsolidationResult{{
			Operation:  "DELETE",
			OriginalID: "p1",
			Reason:     "r",
			Evidence:   []string{evidenceID},
		}})
		if len(valid) != 1 || rejected != 0 {
			t.Errorf("evidence %q: valid=%d rejected=%d, want 1/0", evidenceID, len(valid), rejected)
		}
	}
}

// TestValidateResultsAcceptsInBatchPrincipleEvidence: citing a principle from
// the batch is allowed even though it is not an event/session — it is the row
// under consideration, and the prompt tells the model to use those IDs.
func TestValidateResultsAcceptsInBatchPrincipleEvidence(t *testing.T) {
	database := openApplyTestDB(t)
	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}, {ID: "p2", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{{
		Operation:  "UPDATE",
		OriginalID: "p1",
		Title:      "t",
		Narrative:  "n",
		Reason:     "r",
		Evidence:   []string{"p2"},
	}})
	if len(valid) != 1 || rejected != 0 {
		t.Fatalf("valid=%d rejected=%d, want 1/0", len(valid), rejected)
	}
}

func TestLoadPrinciplesForConsolidation(t *testing.T) {
	database := openApplyTestDB(t)

	seed := []*db.Principle{
		{Title: "a-high", Narrative: "n", ProjectID: "proj-a", Type: "pattern", ImpactScore: 0.9},
		{Title: "a-low", Narrative: "n", ProjectID: "proj-a", Type: "pattern", ImpactScore: 0.4},
		{Title: "b-mid", Narrative: "n", ProjectID: "proj-b", Type: "pattern", ImpactScore: 0.7},
	}
	for _, p := range seed {
		if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
			t.Fatalf("InsertPrinciple(%s): inserted=%v err=%v", p.Title, inserted, err)
		}
	}

	all, err := loadPrinciplesForConsolidation(database, "", 0)
	if err != nil {
		t.Fatalf("loadPrinciplesForConsolidation: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all: len=%d, want 3", len(all))
	}
	if all[0].Title != "a-high" || all[1].Title != "b-mid" {
		t.Errorf("all: order = %s, %s; want a-high, b-mid", all[0].Title, all[1].Title)
	}

	capped, err := loadPrinciplesForConsolidation(database, "", 2)
	if err != nil {
		t.Fatalf("loadPrinciplesForConsolidation(limit): %v", err)
	}
	if len(capped) != 2 || capped[0].Title != "a-high" || capped[1].Title != "b-mid" {
		t.Errorf("limit 2: %v", []string{capped[0].Title, capped[1].Title})
	}

	scoped, err := loadPrinciplesForConsolidation(database, "proj-a", 0)
	if err != nil {
		t.Fatalf("loadPrinciplesForConsolidation(project): %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("project proj-a: len=%d, want 2", len(scoped))
	}
	for _, p := range scoped {
		if p.ProjectID != "proj-a" {
			t.Errorf("project filter leaked %q", p.ProjectID)
		}
	}

	over, err := loadPrinciplesForConsolidation(database, "", 99)
	if err != nil || len(over) != 3 {
		t.Errorf("limit above count: len=%d err=%v, want 3/nil", len(over), err)
	}
}

func TestApplyResultsPersistsScope(t *testing.T) {
	database := openApplyTestDB(t)

	if err := applyBatch(t, database, ConsolidationResult{
		Operation:   "ADD",
		NewID:       "global-add",
		Title:       "portable lesson",
		Narrative:   "applies everywhere",
		ProjectID:   "proj",
		Type:        "pattern",
		ImpactScore: 0.7,
		Scope:       "global",
		Reason:      "r",
	}); err != nil {
		t.Fatalf("applyResults ADD: %v", err)
	}
	got, err := database.GetPrincipleByID("global-add")
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID: got=%v err=%v", got, err)
	}
	if got.Scope != "global" {
		t.Errorf("ADD scope = %q, want global", got.Scope)
	}

	if err := applyBatch(t, database, ConsolidationResult{
		Operation:   "UPDATE",
		OriginalID:  "global-add",
		Title:       "portable lesson",
		Narrative:   "now project specific",
		ImpactScore: 0.7,
		Scope:       "project",
		Reason:      "r",
	}); err != nil {
		t.Fatalf("applyResults UPDATE: %v", err)
	}
	got, err = database.GetPrincipleByID("global-add")
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID after update: got=%v err=%v", got, err)
	}
	if got.Scope != "project" {
		t.Errorf("UPDATE scope = %q, want project", got.Scope)
	}
}

func TestValidateResultsScopeHandling(t *testing.T) {
	database := openApplyTestDB(t)
	batch := []db.Principle{{ID: "p1", ProjectID: "proj"}}

	valid, rejected := validateOn(t, database, batch, []ConsolidationResult{
		{Operation: "UPDATE", OriginalID: "p1", Title: "t", Narrative: "n", Reason: "r", Evidence: []string{"p1"}}, // empty -> project
		{Operation: "UPDATE", OriginalID: "p1", Title: "t", Narrative: "n", Reason: "r", Evidence: []string{"p1"}, Scope: "everywhere"},
	})
	if rejected != 1 || len(valid) != 1 {
		t.Fatalf("valid=%d rejected=%d, want 1/1", len(valid), rejected)
	}
	if valid[0].Scope != "project" {
		t.Errorf("defaulted scope = %q, want project", valid[0].Scope)
	}
}
