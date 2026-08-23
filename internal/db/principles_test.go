package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/security"
)

func openPrinciplesDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestScanPrinciplesDropsTamperedRow proves the actual threat this hardening
// pass targets: a principle whose narrative was altered directly in SQLite
// (bypassing InsertPrinciple, e.g. a local attacker with filesystem write
// access) must not survive a read — its HMAC no longer matches.
func TestScanPrinciplesDropsTamperedRow(t *testing.T) {
	database := openPrinciplesDB(t)

	p := &Principle{Title: "t", Narrative: "original", ProjectID: "proj"}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}
	if hmacKey() == nil {
		t.Skip("no signing key available in this environment (keychain + file fallback both failed)")
	}
	if p.Signature == "" {
		t.Fatal("expected InsertPrinciple to set a signature when a key is available")
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID before tamper: got=%v err=%v", got, err)
	}

	// Simulate direct filesystem/SQL tampering that bypasses signing.
	if _, err := database.conn.Exec(`UPDATE principles SET narrative = 'tampered' WHERE id = ?`, p.ID); err != nil {
		t.Fatalf("simulate tamper: %v", err)
	}

	got, err = database.GetPrincipleByID(p.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID after tamper: %v", err)
	}
	if got != nil {
		t.Fatalf("expected tampered principle to be dropped, got %+v", got)
	}
}

// TestPrincipleNarrativeStoredEncrypted proves the P2 "Encryption at Rest"
// gap is closed for principles: the raw column a filesystem-level dump would
// see (`strings forge.db`) never contains the plaintext narrative, while the
// normal read path (GetPrincipleByID) still returns it decrypted.
func TestPrincipleNarrativeStoredEncrypted(t *testing.T) {
	database := openPrinciplesDB(t)
	if hmacKey() == nil {
		t.Skip("no signing/encryption key available in this environment")
	}

	const secret = "api-key-should-never-be-plaintext-on-disk"
	p := &Principle{Title: "t", Narrative: secret, ProjectID: "proj"}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}

	var rawNarrative string
	if err := database.conn.QueryRow(`SELECT narrative FROM principles WHERE id = ?`, p.ID).Scan(&rawNarrative); err != nil {
		t.Fatalf("read raw narrative column: %v", err)
	}
	if strings.Contains(rawNarrative, secret) {
		t.Fatalf("raw stored narrative contains plaintext: %q", rawNarrative)
	}
	if !strings.HasPrefix(rawNarrative, security.EncPrefix) {
		t.Fatalf("expected raw narrative to carry encryption prefix, got %q", rawNarrative)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID: got=%v err=%v", got, err)
	}
	if got.Narrative != secret {
		t.Fatalf("app-layer read should decrypt narrative: got %q want %q", got.Narrative, secret)
	}
}

// TestScanPrinciplesDropsUndecryptableNarrative proves a narrative ciphertext
// tampered directly at the SQL/filesystem layer is dropped on read, the same
// way a bad HMAC signature is — decryption failure and signature failure are
// both "this row cannot be trusted", not just one of them.
func TestScanPrinciplesDropsUndecryptableNarrative(t *testing.T) {
	database := openPrinciplesDB(t)
	if hmacKey() == nil {
		t.Skip("no signing/encryption key available in this environment")
	}

	p := &Principle{Title: "t", Narrative: "original", ProjectID: "proj"}
	inserted, err := database.InsertPrinciple(p)
	if err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}

	// Corrupt the ciphertext directly, simulating filesystem tampering that
	// bypasses Encrypt entirely.
	if _, err := database.conn.Exec(
		`UPDATE principles SET narrative = ? WHERE id = ?`, security.EncPrefix+"tamperedgarbage", p.ID,
	); err != nil {
		t.Fatalf("simulate tamper: %v", err)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID after tamper: %v", err)
	}
	if got != nil {
		t.Fatalf("expected principle with undecryptable narrative to be dropped, got %+v", got)
	}
}

func TestRevokePrincipleExcludesFromActiveReads(t *testing.T) {
	database := openPrinciplesDB(t)
	p := &Principle{Title: "t", Narrative: "n", ProjectID: "proj"}
	if _, err := database.InsertPrinciple(p); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	if err := database.RevokePrinciple(p.ID); err != nil {
		t.Fatalf("RevokePrinciple: %v", err)
	}

	active, err := database.RecentPrinciplesByProject("proj", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProject: %v", err)
	}
	for _, got := range active {
		if got.ID == p.ID {
			t.Fatalf("revoked principle %s still returned by an active-only read", p.ID)
		}
	}
}

// TestResignAllPrinciplesSurvivesKeyRotation proves the actual rotation
// scenario: principles signed under an old key must still verify after
// ResignAllPrinciples re-signs them with a new one — otherwise rotating the
// key would silently drop every existing principle on next read.
func TestResignAllPrinciplesSurvivesKeyRotation(t *testing.T) {
	database := openPrinciplesDB(t)
	if hmacKey() == nil {
		t.Skip("no signing key available in this environment")
	}

	p := &Principle{Title: "t", Narrative: "n", ProjectID: "proj"}
	if _, err := database.InsertPrinciple(p); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	oldKey := hmacKey()
	newKey := []byte("rotated-key-32-bytes-of-padding!")
	resigned, quarantined, err := database.ResignAllPrinciples(oldKey, newKey)
	if err != nil {
		t.Fatalf("ResignAllPrinciples: %v", err)
	}
	if resigned != 1 || quarantined != 0 {
		t.Fatalf("ResignAllPrinciples resigned=%d quarantined=%d, want 1,0", resigned, quarantined)
	}

	var sig string
	if err := database.conn.QueryRow(`SELECT signature FROM principles WHERE id = ?`, p.ID).Scan(&sig); err != nil {
		t.Fatalf("read signature: %v", err)
	}
	if !security.Verify(newKey, signedPayload(p.Title, p.Narrative), sig) {
		t.Fatal("principle signature does not verify under the new key after rotation")
	}
}

// TestResignAllPrinciplesQuarantinesTamperedRows proves the bug this test
// guards against: rotation must not blindly re-sign whatever content
// currently sits in a row. A row tampered before rotation (fails
// verification under the old key) has to be quarantined, not legitimized
// under the new key — otherwise key rotation silently undoes tamper
// detection instead of being a compromise-recovery tool.
func TestResignAllPrinciplesQuarantinesTamperedRows(t *testing.T) {
	database := openPrinciplesDB(t)
	oldKey := hmacKey()
	if oldKey == nil {
		t.Skip("no signing key available in this environment")
	}

	p := &Principle{Title: "t", Narrative: "original", ProjectID: "proj"}
	if _, err := database.InsertPrinciple(p); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if _, err := database.conn.Exec(`UPDATE principles SET narrative = 'tampered' WHERE id = ?`, p.ID); err != nil {
		t.Fatalf("simulate tamper: %v", err)
	}

	newKey := []byte("rotated-key-32-bytes-of-padding!")
	resigned, quarantined, err := database.ResignAllPrinciples(oldKey, newKey)
	if err != nil {
		t.Fatalf("ResignAllPrinciples: %v", err)
	}
	if resigned != 0 || quarantined != 1 {
		t.Fatalf("ResignAllPrinciples resigned=%d quarantined=%d, want 0,1", resigned, quarantined)
	}

	got, err := database.GetPrincipleByID(p.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if got != nil {
		t.Fatalf("expected quarantined (revoked) principle to be excluded from reads, got %+v", got)
	}

	var status string
	if err := database.conn.QueryRow(`SELECT status FROM principles WHERE id = ?`, p.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "revoked" {
		t.Fatalf("status = %q, want %q", status, "revoked")
	}
}

func TestFilterConcepts(t *testing.T) {
	cases := []struct {
		input []string
		want  []string
	}{
		{[]string{"security", "unknown", "pattern"}, []string{"security", "pattern"}},
		{[]string{"GOTCHA", " Performance "}, []string{"gotcha", "performance"}},
		{[]string{"not-a-concept"}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		got := FilterConcepts(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("FilterConcepts(%v) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("FilterConcepts(%v)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		terms []string
		want  string
	}{
		{[]string{"rust", "error"}, `"rust" OR "error"`},
		{[]string{"a term"}, `"a term"`},
		{[]string{"", "foo"}, `"foo"`},
		{nil, ""},
	}
	for _, tc := range cases {
		got := buildFTSQuery(tc.terms)
		if got != tc.want {
			t.Errorf("buildFTSQuery(%v) = %q, want %q", tc.terms, got, tc.want)
		}
	}
}

func TestProjectIDSelectors(t *testing.T) {
	exact, unix, win := projectIDSelectors("/home/user/myproject")
	if exact != "/home/user/myproject" {
		t.Errorf("exact = %q", exact)
	}
	if unix != "%/myproject" {
		t.Errorf("unix = %q", unix)
	}
	if win != `%\myproject` {
		t.Errorf("win = %q", win)
	}
}

func TestEncodeDecodeStringSlice(t *testing.T) {
	input := []string{"a", "b", "c"}
	encoded := encodeStringSlice(input)
	decoded := decodeStringSlice(encoded)
	if len(decoded) != len(input) {
		t.Fatalf("len = %d, want %d", len(decoded), len(input))
	}
	for i, v := range decoded {
		if v != input[i] {
			t.Errorf("[%d] = %q, want %q", i, v, input[i])
		}
	}
	if encodeStringSlice(nil) != "" {
		t.Error("expected empty string for nil slice")
	}
	if decodeStringSlice("") != nil {
		t.Error("expected nil for empty string")
	}
}

func TestFingerprint(t *testing.T) {
	f1 := fingerprint("same title", "proj-a")
	f2 := fingerprint("same title", "proj-a")
	if f1 != f2 {
		t.Errorf("fingerprint not stable: %q vs %q", f1, f2)
	}
	f3 := fingerprint("different title", "proj-a")
	if f1 == f3 {
		t.Error("different titles should produce different fingerprints")
	}
}

func TestRecentPrinciplesByProjectAll(t *testing.T) {
	db := openPrinciplesDB(t)

	if _, err := db.InsertPrinciple(&Principle{
		Type:      "pattern",
		Title:     "alpha insight",
		Narrative: "narrative a",
		ProjectID: "/home/user/proj-alpha",
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if _, err := db.InsertPrinciple(&Principle{
		Type:      "pattern",
		Title:     "beta insight",
		Narrative: "narrative b",
		ProjectID: "/home/user/proj-beta",
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	// All results for proj-alpha
	all, err := db.RecentPrinciplesByProjectAll("/home/user/proj-alpha", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProjectAll: %v", err)
	}
	if len(all) != 1 || all[0].Title != "alpha insight" {
		t.Errorf("unexpected results: %+v", all)
	}
}

func TestRecentPrinciplesByProjectAll_EmptyProjectReturnsAll(t *testing.T) {
	db := openPrinciplesDB(t)
	for i, title := range []string{"first", "second"} {
		if _, err := db.InsertPrinciple(&Principle{
			Type:      "pattern",
			Title:     title,
			Narrative: "narrative",
			ProjectID: "proj-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("InsertPrinciple: %v", err)
		}
	}
	all, err := db.RecentPrinciplesByProjectAll("", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProjectAll(empty): %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected >= 2 results, got %d", len(all))
	}
}

func TestActiveOnly(t *testing.T) {
	db := openPrinciplesDB(t)
	principles := []Principle{
		{Status: "active", Title: "keep-active"},
		{Status: "", Title: "keep-legacy"},
		{Status: "conflicting", Title: "skip-conflicting"},
	}
	got, err := db.activeOnly(principles, nil)
	if err != nil {
		t.Fatalf("activeOnly: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("activeOnly = %d, want 2", len(got))
	}
	for _, p := range got {
		if p.Status == "conflicting" {
			t.Errorf("conflicting principle leaked: %+v", p)
		}
	}
}

func TestActiveOnly_PropagatesError(t *testing.T) {
	db := openPrinciplesDB(t)
	testErr := &testError{"injected error"}
	_, err := db.activeOnly(nil, testErr)
	if err != testErr {
		t.Errorf("expected propagated error, got %v", err)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestSessionEventsDB(t *testing.T) {
	db := openPrinciplesDB(t)

	for i := 0; i < 3; i++ {
		if err := db.InsertEvent(&Event{
			SessionID:  "sess-target",
			ProjectID:  "proj",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    "{}",
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	if err := db.InsertEvent(&Event{
		SessionID:  "other-sess",
		ProjectID:  "proj",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent other: %v", err)
	}

	events, err := db.SessionEvents("sess-target", 10)
	if err != nil {
		t.Fatalf("SessionEvents: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("len(events) = %d, want 3", len(events))
	}
}

func TestSearchEventsByProject(t *testing.T) {
	db := openPrinciplesDB(t)

	if err := db.InsertEvent(&Event{
		SessionID:  "s1",
		ProjectID:  "proj-search",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "unique-token-xyz search target",
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if err := db.InsertEvent(&Event{
		SessionID:  "s2",
		ProjectID:  "proj-other",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "unique-token-xyz search target",
	}); err != nil {
		t.Fatalf("InsertEvent other: %v", err)
	}

	events, err := db.SearchEventsByProject("proj-search", "unique-token-xyz", 10)
	if err != nil {
		t.Fatalf("SearchEventsByProject: %v", err)
	}
	for _, e := range events {
		if e.ProjectID != "proj-search" {
			t.Errorf("unexpected project_id %q in results", e.ProjectID)
		}
	}
}

func TestSearchEventsByProject_EmptyFallsBackToGlobal(t *testing.T) {
	db := openPrinciplesDB(t)
	if err := db.InsertEvent(&Event{
		SessionID:  "s1",
		ProjectID:  "any-proj",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "global-search-term-abc",
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	events, err := db.SearchEventsByProject("", "global-search-term-abc", 10)
	if err != nil {
		t.Fatalf("SearchEventsByProject(empty): %v", err)
	}
	if len(events) == 0 {
		t.Error("expected at least 1 result for global search")
	}
}

func TestProjectAgents(t *testing.T) {
	db := openPrinciplesDB(t)
	for _, tool := range []string{"claude", "gemini", "claude"} {
		if err := db.InsertEvent(&Event{
			SessionID:  "s",
			ProjectID:  "proj-agents",
			SourceTool: tool,
			EventType:  "PostToolUse",
			Payload:    "{}",
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	agents, err := db.ProjectAgents("proj-agents")
	if err != nil {
		t.Fatalf("ProjectAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("agents = %v, want [claude gemini]", agents)
	}
}

func TestPrincipleScoreAndUsage(t *testing.T) {
	db := openPrinciplesDB(t)

	p := &Principle{
		ID:          "p-test-1",
		TS:          "2026-06-12T10:00:00Z",
		Type:        "bugfix",
		Title:       "Test Title",
		Narrative:   "Test Narrative",
		ImpactScore: 0.5,
		ProjectID:   "proj-test",
		Fingerprint: "fingerprint-1",
	}

	inserted, err := db.InsertPrinciple(p)
	if err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if !inserted {
		t.Fatal("expected principle to be inserted")
	}

	// Test UpdatePrincipleScore
	if err := db.UpdatePrincipleScore("p-test-1", 0.85); err != nil {
		t.Fatalf("UpdatePrincipleScore: %v", err)
	}

	// Test IncrementPrincipleUsage
	if err := db.IncrementPrincipleUsage("p-test-1"); err != nil {
		t.Fatalf("IncrementPrincipleUsage: %v", err)
	}

	fetched, err := db.GetPrincipleByID("p-test-1")
	if err != nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if fetched == nil {
		t.Fatal("expected principle to be found")
	}

	if fetched.ImpactScore != 0.85 {
		t.Errorf("expected impact score 0.85, got %v", fetched.ImpactScore)
	}
	if fetched.UseCount != 1 {
		t.Errorf("expected use count 1, got %v", fetched.UseCount)
	}
	if fetched.LastUsedTS == "" {
		t.Error("expected last used ts to be set")
	}
}
