package streams

import (
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func rng(sessionID string, lo, hi time.Time, events int) db.SessionRange {
	return db.SessionRange{SessionID: sessionID, Lo: lo.Format(time.RFC3339), Hi: hi.Format(time.RFC3339), Events: events}
}

func TestCluster(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ranges := []db.SessionRange{
		rng("a", base, base.Add(time.Hour), 10),
		// same day, small gap — same stream
		rng("b", base.Add(3*time.Hour), base.Add(4*time.Hour), 5),
		// next day, within 48h gap — same stream
		rng("c", base.Add(26*time.Hour), base.Add(27*time.Hour), 8),
		// a week later — new stream
		rng("d", base.Add(7*24*time.Hour), base.Add(7*24*time.Hour+time.Hour), 3),
	}

	got, err := Cluster(ranges, DefaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d streams, want 2: %+v", len(got), got)
	}
	if len(got[0].SessionIDs) != 3 {
		t.Errorf("stream 0 should merge a,b,c (%d sessions), got %v", len(got[0].SessionIDs), got[0].SessionIDs)
	}
	if got[0].Events != 23 {
		t.Errorf("stream 0 events = %d, want 23", got[0].Events)
	}
	if len(got[1].SessionIDs) != 1 || got[1].SessionIDs[0] != "d" {
		t.Errorf("stream 1 should be just [d], got %v", got[1].SessionIDs)
	}
}

func TestCluster_Empty(t *testing.T) {
	got, err := Cluster(nil, DefaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no streams, got %d", len(got))
	}
}

func TestCluster_ExactlyAtGapBoundary(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ranges := []db.SessionRange{
		rng("a", base, base.Add(time.Hour), 1),
		// gap is exactly DefaultGap after end of a -> not > gap, same stream
		rng("b", base.Add(time.Hour).Add(DefaultGap), base.Add(time.Hour).Add(DefaultGap).Add(time.Minute), 1),
	}
	got, err := Cluster(ranges, DefaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("gap exactly at threshold should still merge, got %d streams", len(got))
	}
}
