package db

import "testing"

func TestEvidenceExists(t *testing.T) {
	database := openPrinciplesDB(t)

	ev := &Event{
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
	summary := &SessionSummary{SessionID: "sess-real-1", ProjectID: "proj", Summary: "did work"}
	if err := database.InsertSessionSummary(summary); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}

	cases := []struct {
		id   string
		want bool
	}{
		{"", false},
		{"evt-does-not-exist", false},
		{ev.ID, true},
		{summary.ID, true},
		{summary.SessionID, true},
		{"principle-id-is-not-external-evidence", false},
	}
	for _, tc := range cases {
		got, err := database.EvidenceExists(tc.id)
		if err != nil {
			t.Fatalf("EvidenceExists(%q): %v", tc.id, err)
		}
		if got != tc.want {
			t.Errorf("EvidenceExists(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
