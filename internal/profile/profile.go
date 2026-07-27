// Package profile builds a builder profile (5-axis score) from a project's
// already-computed local signals — commits, work streams, steering rate, and
// session summaries — via a single LLM call. No new data collection: it
// reuses what forge commits/streams/steering/distill already produce.
package profile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Axis is one scored dimension with its one-line justification.
type Axis struct {
	Score int    `json:"score"`
	Why   string `json:"why"`
}

// Scores is the 5-axis builder profile.
type Scores struct {
	Steering        Axis `json:"steering"`
	Execution       Axis `json:"execution"`
	Engineering     Axis `json:"engineering"`
	ProductInstinct Axis `json:"product_instinct"`
	Planning        Axis `json:"planning"`
}

// Signals is the local evidence digest fed to the LLM.
type Signals struct {
	ProjectID       string
	TotalCommits    int
	TotalInsertions int
	TotalDeletions  int
	WorkStreams     int
	TotalPrompts    int
	SteeredPrompts  int
	SessionNotes    []string // recent session summaries/learnings, most recent first
}

// BuildPrompt renders the signals into an LLM prompt requesting strict JSON
// scores on 5 axes, each 1-10 with a one-sentence rationale grounded in the
// given evidence.
func BuildPrompt(s Signals) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are scoring a software builder's working habits on project %q using only the evidence below.\n\n", s.ProjectID)
	fmt.Fprintf(&sb, "Evidence:\n")
	fmt.Fprintf(&sb, "- %d commits, +%d/-%d lines, across %d work streams\n", s.TotalCommits, s.TotalInsertions, s.TotalDeletions, s.WorkStreams)
	if s.TotalPrompts > 0 {
		fmt.Fprintf(&sb, "- %d/%d prompts interrupted the agent mid-task (steering rate %.0f%%)\n",
			s.SteeredPrompts, s.TotalPrompts, 100*float64(s.SteeredPrompts)/float64(s.TotalPrompts))
	}
	if len(s.SessionNotes) > 0 {
		fmt.Fprintf(&sb, "- Recent session notes:\n")
		for _, n := range s.SessionNotes {
			fmt.Fprintf(&sb, "  - %s\n", n)
		}
	}
	sb.WriteString(`
Score these 5 axes from 1-10, each with a one-sentence "why" grounded in the
evidence above (no generic praise, no invented facts):
- steering: how deliberately the user directs the agent vs. lets it wander
- execution: follow-through, shipping cadence, finishing what's started
- engineering: code quality signal from commit patterns (scope, size, focus)
- product_instinct: judgment on what to build/skip
- planning: evidence of upfront structure vs. reactive thrashing

Respond with ONLY this JSON shape, no prose, no markdown fences:
{"steering":{"score":N,"why":"..."},"execution":{"score":N,"why":"..."},"engineering":{"score":N,"why":"..."},"product_instinct":{"score":N,"why":"..."},"planning":{"score":N,"why":"..."}}
`)
	return sb.String()
}

var jsonBlock = regexp.MustCompile(`(?s)\{.*\}`)

// ParseScores extracts the JSON scores object from an LLM response,
// tolerating surrounding prose or markdown code fences.
func ParseScores(resp string) (Scores, error) {
	m := jsonBlock.FindString(resp)
	if m == "" {
		return Scores{}, fmt.Errorf("no JSON object found in response")
	}
	var s Scores
	if err := json.Unmarshal([]byte(m), &s); err != nil {
		return Scores{}, fmt.Errorf("parsing scores JSON: %w", err)
	}
	return s, nil
}
