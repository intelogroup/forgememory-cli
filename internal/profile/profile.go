// Package profile builds a builder profile from a project's local signals —
// commits (with file-level hygiene), work streams, steering rate, tool-usage
// patterns, verification discipline, and raw prompt samples — via a single
// LLM call scored against a concrete engineering rubric. No new data
// collection: it reuses what forge commits/streams/steering/distill already
// produce, plus one direct git-log read for file paths.
package profile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/funstats"
)

// Axis is one scored dimension with its one-line justification. NA marks a
// deterministic axis with no applicable evidence (e.g. Engineering with 0
// commits) — Score should not be printed or averaged when NA is true.
type Axis struct {
	Score int    `json:"score"`
	Why   string `json:"why"`
	NA    bool   `json:"-"`
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

	AvgFilesPerCommit   float64 // commit atomicity: low = focused, high = sprawling
	TestPairedCommitPct float64 // % of code commits that also touch a test file

	TotalSessions    int
	VerifiedSessions int            // sessions with at least one test-runner tool call
	ToolCounts       map[string]int // PostToolUse tool_name -> count

	PromptSamples []string // representative raw prompt text, most recent first
}

var testTouchedPath = regexp.MustCompile(`(?i)(_test\.|\.test\.|/tests?/|^tests?/)`)

// CommitHygiene computes atomicity signals from a commit's file paths:
// avgFilesPerCommit (lower = more focused changes) and the fraction of
// commits that pair a test-file edit with a non-test code edit (test
// coverage discipline, not just line-count churn).
func CommitHygiene(pathsPerCommit [][]string) (avgFiles float64, testPairedPct float64) {
	if len(pathsPerCommit) == 0 {
		return 0, 0
	}
	totalFiles, paired := 0, 0
	for _, paths := range pathsPerCommit {
		totalFiles += len(paths)
		hasTest, hasCode := false, false
		for _, p := range paths {
			if testTouchedPath.MatchString(p) {
				hasTest = true
			} else {
				hasCode = true
			}
		}
		if hasTest && hasCode {
			paired++
		}
	}
	avgFiles = float64(totalFiles) / float64(len(pathsPerCommit))
	testPairedPct = 100 * float64(paired) / float64(len(pathsPerCommit))
	return avgFiles, testPairedPct
}

// ComputeScores derives the Engineering and Steering axis scores directly
// from local signals — the two axes with a clean deterministic proxy — so
// they're reproducible across runs instead of an LLM picking the digit.
// Engineering is NA when there are no commits in the observed window (atomicity
// and test-pairing aren't measurable, not evidence of poor discipline).
func ComputeScores(s Signals, errs funstats.ErrorProfile) (engineering, steering Axis) {
	if s.TotalCommits == 0 {
		engineering.NA = true
	} else {
		verifiedRate := 0.0
		if s.TotalSessions > 0 {
			verifiedRate = float64(s.VerifiedSessions) / float64(s.TotalSessions)
		}
		vibe := funstats.VibeCoderScore(0, verifiedRate, errs)
		atomicity := 1.0
		if s.AvgFilesPerCommit > 5 {
			atomicity = 5 / s.AvgFilesPerCommit
			if atomicity < 0 {
				atomicity = 0
			}
		}
		pairing := s.TestPairedCommitPct / 100
		score := 100 * (0.4*atomicity + 0.3*pairing + 0.3*(vibe/100))
		engineering.Score = scaleToTen(score)
	}

	steeringRate := 0.0
	if s.TotalPrompts > 0 {
		steeringRate = float64(s.SteeredPrompts) / float64(s.TotalPrompts)
	}
	steering.Score = scaleToTen(100 * (1 - steeringRate))
	return engineering, steering
}

func scaleToTen(pct float64) int {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	n := int(pct/10 + 0.5)
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}
	return n
}

var testRunPattern = regexp.MustCompile(`(?i)\b(go test|npm test|npm run test|yarn test|pnpm test|pytest|jest|vitest|cargo test|mvn test|gradle test|rspec|dotnet test)\b`)

// ClassifyEvents tallies PostToolUse tool usage and detects whether any tool
// call in the session ran a test suite — the closest local proxy we have for
// "verified before trusting" rather than shipping on the first green build.
func ClassifyEvents(events []db.Event) (toolCounts map[string]int, hasTestRun bool) {
	toolCounts = map[string]int{}
	for _, e := range events {
		if e.EventType != "PostToolUse" && e.EventType != "postToolUse" {
			continue
		}
		if e.ToolName != "" {
			toolCounts[e.ToolName]++
		}
		if testRunPattern.MatchString(e.Payload) {
			hasTestRun = true
		}
	}
	return toolCounts, hasTestRun
}

// BuildPrompt renders the signals into an LLM prompt that scores 5 axes
// against a concrete engineering rubric, grounded only in the evidence
// given — not vague adjectives.
func BuildPrompt(s Signals) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are a principal engineer assessing a software builder's habits on project %q using ONLY the evidence below. Do not invent facts. Do not give generic praise — every rationale must cite a specific number from the evidence.\n\n", s.ProjectID)

	sb.WriteString("Evidence:\n")
	fmt.Fprintf(&sb, "- %d commits, +%d/-%d lines, across %d work streams\n", s.TotalCommits, s.TotalInsertions, s.TotalDeletions, s.WorkStreams)
	if s.TotalCommits == 0 {
		sb.WriteString("- No commits in the observed window (this may be a non-git working directory, or work not yet committed) — do NOT penalize engineering/commit-hygiene axes for this; treat commit atomicity and test-pairing as not applicable rather than as evidence of poor discipline.\n")
	} else {
		fmt.Fprintf(&sb, "- Commit atomicity: avg %.1f files/commit (industry norm for focused, reviewable changes: 1-5)\n", s.AvgFilesPerCommit)
		fmt.Fprintf(&sb, "- Test-coverage discipline: %.0f%% of commits pair a test-file edit with a code edit\n", s.TestPairedCommitPct)
	}
	if s.TotalSessions > 0 {
		fmt.Fprintf(&sb, "- Verification discipline: %d/%d sessions (%.0f%%) ran a test suite (go test/npm test/pytest/etc.) via tool calls before ending\n",
			s.VerifiedSessions, s.TotalSessions, 100*float64(s.VerifiedSessions)/float64(s.TotalSessions))
	}
	if s.TotalPrompts > 0 {
		fmt.Fprintf(&sb, "- %d/%d prompts interrupted the agent mid-task (steering rate %.0f%%)\n",
			s.SteeredPrompts, s.TotalPrompts, 100*float64(s.SteeredPrompts)/float64(s.TotalPrompts))
	}
	if len(s.ToolCounts) > 0 {
		fmt.Fprintf(&sb, "- Tool usage: %s\n", formatToolCounts(s.ToolCounts))
	}
	if len(s.PromptSamples) > 0 {
		sb.WriteString("- Representative raw prompts (verbatim, judge specificity/clarity from these directly):\n")
		for _, p := range s.PromptSamples {
			fmt.Fprintf(&sb, "  - %q\n", truncate(p, 200))
		}
	}
	if len(s.SessionNotes) > 0 {
		sb.WriteString("- Recent session notes:\n")
		for _, n := range s.SessionNotes {
			fmt.Fprintf(&sb, "  - %s\n", n)
		}
	}

	sb.WriteString(`
Score execution, product_instinct, and planning from 1-10 using the rubric
below. Each "why" must name the evidence line that justified the score.

steering and engineering are scored deterministically elsewhere from the raw
numbers — you do NOT pick their digit (put 0 in "score" for both, it is
discarded). You still write their "why": explain what the numbers mean,
using the prompt samples to tell whether a low steering rate reflects trust
from clear upfront direction, or disengagement.

- execution: shipping cadence and follow-through across work streams
- engineering: weighted from commit atomicity (fewer files/commit is better,
  >10 avg is a red flag for sprawling changes) AND test-coverage discipline
  AND verification discipline (running tests before trusting output is a
  standard senior-engineer practice — near-0% is a real gap, not neutral)
- product_instinct: judgment on what to build/skip, visible in prompts/notes
- planning: evidence of upfront structure vs. reactive thrashing
- steering: prompt specificity and directive clarity

Respond with ONLY this JSON shape, no prose, no markdown fences:
{"steering":{"score":0,"why":"..."},"execution":{"score":N,"why":"..."},"engineering":{"score":0,"why":"..."},"product_instinct":{"score":N,"why":"..."},"planning":{"score":N,"why":"..."}}
`)
	return sb.String()
}

func formatToolCounts(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for name, n := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", name, n))
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
