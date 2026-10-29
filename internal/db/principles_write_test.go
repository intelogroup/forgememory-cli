package db

import (
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/security"
)

// TestUpdatePrincipleContentKeepsRowReadable is the regression test for the
// consolidation path writing title/narrative without refreshing the HMAC:
// scanPrinciples drops any row whose signature no longer matches, so the
// updated principle disappeared on every read.
func TestUpdatePrincipleContentKeepsRowReadable(t *testing.T) {
	database := openPrinciplesDB(t)

	p := &Principle{
		Title:       "use transactions",
		Narrative:   "wrap writes",
		ProjectID:   "proj-a",
		Type:        "pattern",
		ImpactScore: 0.5,
	}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}

	if err := database.UpdatePrincipleContent(
		p.ID,
		"use transactions",
		"wrap multi-row writes in a transaction",
		0.9,
		"BEGIN/COMMIT",
	); err != nil {
		t.Fatalf("UpdatePrincipleContent: %v", err)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if got == nil {
		t.Fatal("updated principle was dropped on read — signature was not refreshed")
	}
	if got.Narrative != "wrap multi-row writes in a transaction" {
		t.Errorf("narrative = %q", got.Narrative)
	}
	if got.ImpactScore != 0.9 {
		t.Errorf("impact = %v, want 0.9", got.ImpactScore)
	}
	if got.ImplHint != "BEGIN/COMMIT" {
		t.Errorf("impl_hint = %q", got.ImplHint)
	}

	if hmacKey() == nil {
		return
	}
	var stored, signature string
	if err := database.conn.QueryRow(
		`SELECT narrative, COALESCE(signature,'') FROM principles WHERE id = ?`, p.ID,
	).Scan(&stored, &signature); err != nil {
		t.Fatalf("read stored row: %v", err)
	}
	if !strings.HasPrefix(stored, security.EncPrefix) {
		t.Errorf("updated narrative stored in plaintext: %q", stored)
	}
	if signature == "" {
		t.Error("expected a refreshed signature")
	}
	if !security.Verify(hmacKey(), signedPayload(got.Title, got.Narrative), signature) {
		t.Error("stored signature does not verify the updated content")
	}
}

func TestUpdatePrincipleContentMissingRow(t *testing.T) {
	database := openPrinciplesDB(t)
	if err := database.UpdatePrincipleContent("does-not-exist", "t", "n", 0.5, ""); err == nil {
		t.Fatal("expected an error for a missing principle")
	}
}

// TestRevivePrincipleIfExpired covers the ADD-after-EXPIRE path: the table
// keeps UNIQUE(fingerprint), so the replacement insert is ignored and the
// expired row must be revived instead of silently lost.
func TestRevivePrincipleIfExpired(t *testing.T) {
	database := openPrinciplesDB(t)

	p := &Principle{
		Title:     "same title",
		Narrative: "v1",
		ProjectID: "proj-a",
		Type:      "pattern",
		Concepts:  []string{"pattern"},
	}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}
	if err := database.ExpirePrinciple(p.ID, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		t.Fatalf("ExpirePrinciple: %v", err)
	}

	// The replacement omits type and concepts on purpose: reviving must keep
	// the expired row's metadata instead of blanking it.
	replacement := &Principle{
		Title:       "same title",
		Narrative:   "v2",
		ProjectID:   "proj-a",
		ImpactScore: 0.8,
	}
	inserted, err = database.InsertPrinciple(replacement)
	if err != nil {
		t.Fatalf("InsertPrinciple replacement: %v", err)
	}
	if inserted {
		t.Fatal("expected the fingerprint collision to be ignored")
	}

	revived, existingID, err := database.RevivePrincipleIfExpired(replacement)
	if err != nil {
		t.Fatalf("RevivePrincipleIfExpired: %v", err)
	}
	if !revived || existingID != p.ID {
		t.Fatalf("revived=%v existingID=%q, want true/%q", revived, existingID, p.ID)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("revived row unreadable: got=%v err=%v", got, err)
	}
	if got.Narrative != "v2" {
		t.Errorf("narrative = %q, want v2", got.Narrative)
	}
	if got.ImpactScore != 0.8 {
		t.Errorf("impact = %v, want 0.8", got.ImpactScore)
	}
	if got.Type != "pattern" {
		t.Errorf("type = %q, want preserved pattern", got.Type)
	}
	if len(got.Concepts) != 1 || got.Concepts[0] != "pattern" {
		t.Errorf("concepts = %v, want preserved [pattern]", got.Concepts)
	}

	var status, expiredAt string
	if err := database.conn.QueryRow(
		`SELECT COALESCE(status,''), COALESCE(expired_at,'') FROM principles WHERE id = ?`, p.ID,
	).Scan(&status, &expiredAt); err != nil {
		t.Fatalf("read revived row: %v", err)
	}
	if status != "active" || expiredAt != "" {
		t.Errorf("status=%q expired_at=%q, want active/empty", status, expiredAt)
	}
}

// TestRevivePrincipleSkipsManualRevocation makes sure an LLM ADD cannot undo
// an explicit `forge harden revoke`: those rows carry status=revoked but no
// expired_at, which is the signal this method keys on.
func TestRevivePrincipleSkipsManualRevocation(t *testing.T) {
	database := openPrinciplesDB(t)

	p := &Principle{Title: "revoked lesson", Narrative: "v1", ProjectID: "proj-a", Type: "pattern"}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}
	if err := database.RevokePrinciple(p.ID); err != nil {
		t.Fatalf("RevokePrinciple: %v", err)
	}

	replacement := &Principle{Title: "revoked lesson", Narrative: "v2", ProjectID: "proj-a", Type: "pattern"}
	if _, err := database.InsertPrinciple(replacement); err != nil {
		t.Fatalf("InsertPrinciple replacement: %v", err)
	}
	revived, existingID, err := database.RevivePrincipleIfExpired(replacement)
	if err != nil {
		t.Fatalf("RevivePrincipleIfExpired: %v", err)
	}
	if revived || existingID != p.ID {
		t.Fatalf("revived=%v existingID=%q, want false/%q", revived, existingID, p.ID)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID: got=%v err=%v", got, err)
	}
	if got.Narrative != "v1" {
		t.Errorf("manual revocation was overwritten: narrative = %q", got.Narrative)
	}
	if got.Status != "revoked" {
		t.Errorf("status = %q, want revoked", got.Status)
	}
}

func TestRevivePrincipleSkipsActiveDuplicate(t *testing.T) {
	database := openPrinciplesDB(t)

	p := &Principle{Title: "active lesson", Narrative: "v1", ProjectID: "proj-a", Type: "pattern"}
	if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}
	replacement := &Principle{Title: "active lesson", Narrative: "v2", ProjectID: "proj-a", Type: "pattern"}
	if _, err := database.InsertPrinciple(replacement); err != nil {
		t.Fatalf("InsertPrinciple replacement: %v", err)
	}

	revived, existingID, err := database.RevivePrincipleIfExpired(replacement)
	if err != nil {
		t.Fatalf("RevivePrincipleIfExpired: %v", err)
	}
	if revived || existingID != p.ID {
		t.Fatalf("revived=%v existingID=%q, want false/%q", revived, existingID, p.ID)
	}
}

// TestExpirePrincipleUnlinksConflictPeer: expiring one half of a conflict pair
// must not strand the other in status='conflicting', where every read path
// hides it and the dashboard only repairs peers that were deleted.
func TestExpirePrincipleUnlinksConflictPeer(t *testing.T) {
	database := openPrinciplesDB(t)

	a := &Principle{Title: "a", Narrative: "na", ProjectID: "proj", Type: "pattern"}
	b := &Principle{Title: "b", Narrative: "nb", ProjectID: "proj", Type: "pattern"}
	for _, p := range []*Principle{a, b} {
		if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
			t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
		}
	}
	if err := database.MarkConflicting(a.ID, b.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	if err := database.ExpirePrinciple(a.ID, "2026-01-02T00:00:00Z", ""); err != nil {
		t.Fatalf("ExpirePrinciple: %v", err)
	}

	got, err := database.GetPrincipleByID(b.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID: got=%v err=%v", got, err)
	}
	if got.Status != "active" || got.ConflictPeerID != "" {
		t.Errorf("peer status=%q conflict_peer_id=%q, want active with no peer", got.Status, got.ConflictPeerID)
	}

	active, err := database.RecentPrinciplesByProject("proj", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProject: %v", err)
	}
	found := false
	for _, p := range active {
		if p.ID == b.ID {
			found = true
		}
	}
	if !found {
		t.Error("peer is still hidden from active reads after the conflict was expired away")
	}
}
