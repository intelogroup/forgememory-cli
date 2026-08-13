package coach

import (
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/skills"
)

func TestParseMode(t *testing.T) {
	for _, test := range []struct {
		input string
		want  Mode
	}{
		{"", ModeObserve},
		{"off", ModeOff},
		{" observe ", ModeObserve},
		{"quiet", ModeQuiet},
		{"normal", ModeNormal},
		{"STRICT", ModeStrict},
	} {
		got, err := ParseMode(test.input)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Errorf("ParseMode(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if _, err := ParseMode("loud"); err == nil {
		t.Fatal("ParseMode(loud) error = nil, want invalid-mode error")
	}
}

func TestQueueEligibleSuppressesLowConfidenceEvidence(t *testing.T) {
	database := openTestDB(t)
	seedSuspectedGap(t, database, "project-a", "low-confidence", 0.69)

	queued, err := QueueEligible(database, "project-a", ModeNormal)
	if err != nil {
		t.Fatalf("QueueEligible: %v", err)
	}
	if queued != 0 {
		t.Fatalf("QueueEligible queued %d items, want 0", queued)
	}
	assertItems(t, database, "project-a", "", 0)
}

func TestQueueEligibleQueuesRepeatedHighConfidenceEvidenceWithDefaultLesson(t *testing.T) {
	database := openTestDB(t)
	seedSuspectedGap(t, database, "project-a", "high-confidence", 0.90)

	queued, err := QueueEligible(database, "project-a", ModeNormal)
	if err != nil {
		t.Fatalf("QueueEligible: %v", err)
	}
	if queued != 1 {
		t.Fatalf("QueueEligible queued %d items, want 1", queued)
	}
	items := listItems(t, database, "project-a", "queued")
	if len(items) != 1 {
		t.Fatalf("queued items = %#v, want one item", items)
	}
	if items[0].Question != "What behavior should the test prove?" {
		t.Errorf("Question = %q", items[0].Question)
	}
	if items[0].NextAction != "Identify one invariant and add the narrowest relevant test." {
		t.Errorf("NextAction = %q", items[0].NextAction)
	}
	if items[0].DeliveryMode != string(ModeNormal) {
		t.Errorf("DeliveryMode = %q, want normal", items[0].DeliveryMode)
	}
}

func TestQueueEligibleHonorsModes(t *testing.T) {
	for _, test := range []struct {
		mode       Mode
		wantQueued int
	}{
		{"", 0},
		{ModeOff, 0},
		{ModeObserve, 0},
		{ModeQuiet, 1},
		{ModeNormal, 1},
		{ModeStrict, 1},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			database := openTestDB(t)
			seedSuspectedGap(t, database, "project-a", "observation-"+string(test.mode), 0.90)

			queued, err := QueueEligible(database, "project-a", test.mode)
			if err != nil {
				t.Fatalf("QueueEligible: %v", err)
			}
			if queued != test.wantQueued {
				t.Fatalf("QueueEligible queued %d items, want %d", queued, test.wantQueued)
			}
			items := listItems(t, database, "project-a", "queued")
			if len(items) != test.wantQueued {
				t.Fatalf("queued item count = %d, want %d", len(items), test.wantQueued)
			}
			if len(items) == 1 && items[0].DeliveryMode != string(test.mode) {
				t.Errorf("DeliveryMode = %q, want %q", items[0].DeliveryMode, test.mode)
			}
		})
	}
}

func TestQueueEligibleDeduplicatesAndStaysWithinProject(t *testing.T) {
	database := openTestDB(t)
	seedSuspectedGap(t, database, "project-a", "observation-a", 0.90)
	seedSuspectedGap(t, database, "project-b", "observation-b", 0.90)

	first, err := QueueEligible(database, "project-a", ModeNormal)
	if err != nil {
		t.Fatalf("first QueueEligible: %v", err)
	}
	second, err := QueueEligible(database, "project-a", ModeNormal)
	if err != nil {
		t.Fatalf("second QueueEligible: %v", err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("QueueEligible counts = %d, %d; want 1, 0", first, second)
	}
	assertItems(t, database, "project-a", "queued", 1)
	assertItems(t, database, "project-b", "", 0)
}

func TestSafeBoundarySuggestionReturnsOneDeliverableItemWithoutMutation(t *testing.T) {
	database := openTestDB(t)
	insertItem(t, database, db.CoachingItem{ID: "quiet", ProjectID: "project-a", SkillKey: "verification.pre_ship", Status: "queued", DeliveryMode: string(ModeQuiet), CreatedAt: "2026-08-13T10:00:00Z"})
	insertItem(t, database, db.CoachingItem{ID: "normal", ProjectID: "project-a", SkillKey: "verification.pre_ship", Status: "queued", DeliveryMode: string(ModeNormal), CreatedAt: "2026-08-13T11:00:00Z"})
	insertItem(t, database, db.CoachingItem{ID: "strict", ProjectID: "project-a", SkillKey: "verification.pre_ship", Status: "queued", DeliveryMode: string(ModeStrict), CreatedAt: "2026-08-13T12:00:00Z"})
	insertItem(t, database, db.CoachingItem{ID: "other-project", ProjectID: "project-b", SkillKey: "verification.pre_ship", Status: "queued", DeliveryMode: string(ModeNormal), CreatedAt: "2026-08-13T13:00:00Z"})

	suggestion, err := SafeBoundarySuggestion(database, "project-a")
	if err != nil {
		t.Fatalf("SafeBoundarySuggestion: %v", err)
	}
	if suggestion == nil || suggestion.ID != "strict" {
		t.Fatalf("SafeBoundarySuggestion = %#v, want strict item", suggestion)
	}
	again, err := SafeBoundarySuggestion(database, "project-a")
	if err != nil || again == nil || again.ID != "strict" {
		t.Fatalf("second SafeBoundarySuggestion = %#v, err=%v", again, err)
	}
	items := listItems(t, database, "project-a", "queued")
	if len(items) != 3 || items[0].ID != "strict" || items[0].SurfacedAt != "" {
		t.Fatalf("selection mutated items: %#v", items)
	}
}

func TestAcceptMovesItemToAcceptedAndSkillToLearning(t *testing.T) {
	database := openTestDB(t)
	seedSuspectedGap(t, database, "project-a", "accepted-observation", 0.90)
	if _, err := QueueEligible(database, "project-a", ModeNormal); err != nil {
		t.Fatalf("QueueEligible: %v", err)
	}
	item := listItems(t, database, "project-a", "queued")[0]

	if err := Accept(database, item.ID); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	accepted := listItems(t, database, "project-a", "accepted")
	if len(accepted) != 1 || accepted[0].ResolvedAt == "" || accepted[0].Resolution != "accepted" {
		t.Fatalf("accepted item = %#v", accepted)
	}
	state, err := database.GetSkillState("verification.pre_ship", skills.ScopeProject, "project-a")
	if err != nil || state == nil || state.State != skills.StateLearning {
		t.Fatalf("skill state after acceptance = %#v, err=%v", state, err)
	}
}

func TestDeferLeavesItemDeferred(t *testing.T) {
	database := openTestDB(t)
	item := queueItem(t, database, "project-a", "deferred-observation")

	if err := Defer(database, item.ID); err != nil {
		t.Fatalf("Defer: %v", err)
	}
	deferred := listItems(t, database, "project-a", "deferred")
	if len(deferred) != 1 || deferred[0].ResolvedAt != "" || deferred[0].Resolution != "deferred" {
		t.Fatalf("deferred item = %#v", deferred)
	}
}

func TestDismissRecordsReasonAndNeverShowAgainSuppressesPattern(t *testing.T) {
	database := openTestDB(t)
	first := queueItem(t, database, "project-a", "dismissed-observation")

	if err := Dismiss(database, first.ID, "never_show_again"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	dismissed := listItems(t, database, "project-a", "dismissed")
	if len(dismissed) != 1 || dismissed[0].ResolvedAt == "" || dismissed[0].Resolution != "never_show_again" {
		t.Fatalf("dismissed item = %#v", dismissed)
	}
	seedSuspectedGap(t, database, "project-a", "new-observation", 0.90)
	queued, err := QueueEligible(database, "project-a", ModeNormal)
	if err != nil {
		t.Fatalf("QueueEligible after dismissal: %v", err)
	}
	if queued != 0 {
		t.Fatalf("QueueEligible queued %d suppressed items, want 0", queued)
	}
	if err := Dismiss(database, first.ID, ""); err == nil {
		t.Fatal("Dismiss with empty reason error = nil, want error")
	}
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedSuspectedGap(t *testing.T, database *db.DB, projectID, observationID string, confidence float64) {
	t.Helper()
	if err := database.InsertObservation(&db.Observation{ID: observationID, CreatedAt: "2026-08-13T12:00:00Z", ProjectID: projectID, Kind: "code_change_without_relevant_test", SkillKey: "verification.pre_ship", Confidence: confidence, Severity: "warning", Status: "active", Summary: "A code change had no detected relevant passing test."}); err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}
	if err := database.UpsertSkillState(&db.SkillState{SkillKey: "verification.pre_ship", ScopeType: skills.ScopeProject, ScopeID: projectID, State: skills.StateSuspectedGap, Confidence: 0.90, EvidenceCount: 2, FailedApplications: 2, UpdatedAt: "2026-08-13T12:00:00Z"}); err != nil {
		t.Fatalf("UpsertSkillState: %v", err)
	}
}

func queueItem(t *testing.T, database *db.DB, projectID, observationID string) db.CoachingItem {
	t.Helper()
	seedSuspectedGap(t, database, projectID, observationID, 0.90)
	if _, err := QueueEligible(database, projectID, ModeNormal); err != nil {
		t.Fatalf("QueueEligible: %v", err)
	}
	items := listItems(t, database, projectID, "queued")
	if len(items) != 1 {
		t.Fatalf("queued items = %#v", items)
	}
	return items[0]
}

func insertItem(t *testing.T, database *db.DB, item db.CoachingItem) {
	t.Helper()
	if err := database.InsertCoachingItem(&item); err != nil {
		t.Fatalf("InsertCoachingItem: %v", err)
	}
}

func listItems(t *testing.T, database *db.DB, projectID, status string) []db.CoachingItem {
	t.Helper()
	items, err := database.ListCoachingItems(projectID, status, 100)
	if err != nil {
		t.Fatalf("ListCoachingItems: %v", err)
	}
	return items
}

func assertItems(t *testing.T, database *db.DB, projectID, status string, want int) {
	t.Helper()
	if got := len(listItems(t, database, projectID, status)); got != want {
		t.Errorf("coaching item count for %s status %q = %d, want %d", projectID, status, got, want)
	}
}
