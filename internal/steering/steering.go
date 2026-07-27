// Package steering measures how often a user redirects the agent mid-task
// instead of waiting for it to finish — a "UserPromptSubmit" with no "Stop"
// (or "SessionEnd") event since the previous prompt in the same session.
package steering

import "github.com/forge/forge/internal/db"

// Stats is one session's (or aggregate) steering measurement.
type Stats struct {
	TotalPrompts   int
	SteeredPrompts int
}

// Rate returns the fraction of prompts that interrupted an in-progress
// agent turn. 0 if there were no prompts.
func (s Stats) Rate() float64 {
	if s.TotalPrompts == 0 {
		return 0
	}
	return float64(s.SteeredPrompts) / float64(s.TotalPrompts)
}

// Add merges another Stats into s (for aggregating across sessions).
func (s *Stats) Add(o Stats) {
	s.TotalPrompts += o.TotalPrompts
	s.SteeredPrompts += o.SteeredPrompts
}

// Compute walks a single session's events in chronological order (the
// contract of db.SessionEventsUpTo/db.SessionEvents ASC ordering) and counts
// prompts that arrived before the agent had stopped from its prior turn.
// The first prompt of a session is never counted as steering — there is
// nothing to interrupt yet.
func Compute(events []db.Event) Stats {
	var s Stats
	stopped := true
	for _, e := range events {
		switch e.EventType {
		case "Stop", "stop", "SessionEnd":
			stopped = true
		case "UserPromptSubmit", "beforeSubmitPrompt":
			s.TotalPrompts++
			if !stopped {
				s.SteeredPrompts++
			}
			stopped = false
		}
	}
	return s
}
