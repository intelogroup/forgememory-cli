// Package streams groups a project's sessions into multi-day work streams —
// runs of sessions close enough in time to represent one continuous push on
// the same problem, rather than unrelated one-off sessions.
package streams

import (
	"time"

	"github.com/forge/forge/internal/db"
)

// WorkStream is a contiguous run of sessions with no gap larger than the
// clustering threshold between consecutive sessions.
type WorkStream struct {
	SessionIDs []string
	Start      time.Time
	End        time.Time
	Events     int
}

// DefaultGap is how much idle time between sessions still counts as the same
// work stream. Paxel-style multi-day streams imply this is generous — a
// day or two of not touching a repo is normal mid-stream, not a new stream.
const DefaultGap = 48 * time.Hour

// Cluster groups session ranges (already sorted by Lo ascending — the
// contract of db.SessionRangesByProject) into work streams using a greedy
// gap threshold: a new session starts a new stream only if it begins more
// than `gap` after the current stream's latest end.
func Cluster(ranges []db.SessionRange, gap time.Duration) ([]WorkStream, error) {
	var streams []WorkStream
	var cur *WorkStream

	for _, r := range ranges {
		lo, err := time.Parse(time.RFC3339, r.Lo)
		if err != nil {
			return nil, err
		}
		hi, err := time.Parse(time.RFC3339, r.Hi)
		if err != nil {
			return nil, err
		}

		if cur != nil && lo.Sub(cur.End) > gap {
			streams = append(streams, *cur)
			cur = nil
		}
		if cur == nil {
			cur = &WorkStream{Start: lo}
		}
		cur.SessionIDs = append(cur.SessionIDs, r.SessionID)
		cur.Events += r.Events
		if hi.After(cur.End) {
			cur.End = hi
		}
	}
	if cur != nil {
		streams = append(streams, *cur)
	}
	return streams, nil
}
