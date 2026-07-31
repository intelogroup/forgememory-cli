// Package funstats computes cheap, purely-aggregate stats over data forge
// already has: peak working hour, top prompt keywords, agent-parallelism,
// and a rule-based "archetype" label built from those.
package funstats

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/forge/forge/internal/db"
)

var stopWords = map[string]bool{
	"that": true, "this": true, "with": true, "have": true, "from": true,
	"just": true, "want": true, "need": true, "please": true, "make": true,
	"like": true, "your": true, "should": true, "would": true, "could": true,
	"also": true, "into": true, "then": true, "them": true, "here": true,
	"what": true, "when": true, "does": true, "will": true,
}

// HourHistogram counts events by hour-of-day (0-23), UTC.
func HourHistogram(events []db.Event) [24]int {
	var hist [24]int
	for _, e := range events {
		t, err := time.Parse(time.RFC3339, e.TS)
		if err != nil {
			continue
		}
		hist[t.UTC().Hour()]++
	}
	return hist
}

// PeakHour returns the hour-of-day (0-23) with the most events.
func PeakHour(events []db.Event) int {
	hist := HourHistogram(events)
	peak := 0
	for h := 1; h < 24; h++ {
		if hist[h] > hist[peak] {
			peak = h
		}
	}
	return peak
}

// TopKeywords returns the n most frequent words (len>=4, stopwords excluded)
// across all UserPromptSubmit event payloads.
func TopKeywords(events []db.Event, n int, extractPrompt func(payload string) string) []string {
	counts := map[string]int{}
	for _, e := range events {
		if e.EventType != "UserPromptSubmit" && e.EventType != "beforeSubmitPrompt" {
			continue
		}
		text := extractPrompt(e.Payload)
		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if len(word) < 4 || stopWords[word] {
				continue
			}
			counts[word]++
		}
	}
	type kv struct {
		word  string
		count int
	}
	var kvs []kv
	for w, c := range counts {
		kvs = append(kvs, kv{w, c})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].count != kvs[j].count {
			return kvs[i].count > kvs[j].count
		}
		return kvs[i].word < kvs[j].word
	})
	if n > len(kvs) {
		n = len(kvs)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = kvs[i].word
	}
	return out
}

// MaxConcurrentSessions sweeps session [lo,hi] windows and returns the peak
// number of sessions with overlapping activity — a proxy for how often the
// user ran multiple agents/sessions in parallel.
func MaxConcurrentSessions(ranges []db.SessionRange) int {
	type point struct {
		ts    time.Time
		delta int
	}
	var points []point
	for _, r := range ranges {
		lo, err := time.Parse(time.RFC3339, r.Lo)
		if err != nil {
			continue
		}
		hi, err := time.Parse(time.RFC3339, r.Hi)
		if err != nil {
			continue
		}
		points = append(points, point{lo, 1}, point{hi, -1})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].ts.Equal(points[j].ts) {
			return points[i].delta > points[j].delta // opens before closes at a tie
		}
		return points[i].ts.Before(points[j].ts)
	})
	cur, max := 0, 0
	for _, p := range points {
		cur += p.delta
		if cur > max {
			max = cur
		}
	}
	return max
}

// Archetype picks a single rule-based label from peak hour, steering rate,
// and concurrency — a lightweight fun summary, not a scored axis.
func Archetype(peakHour int, steeringRate float64, maxConcurrent int) string {
	switch {
	case maxConcurrent >= 3:
		return "Multi-Agent Conductor"
	case peakHour >= 22 || peakHour < 5:
		return "Night Owl"
	case peakHour >= 5 && peakHour < 9:
		return "Early Bird"
	case steeringRate < 0.05:
		return "Autopilot Truster"
	case steeringRate > 0.2:
		return "Hands-On Steerer"
	default:
		return "Steady Builder"
	}
}

// ErrorProfile summarizes a project's failure_signatures history for fair
// judging: it separates normal compiler/test friction from repeated
// unresolved "tool_error" flailing, and tracks how fast errors got resolved.
type ErrorProfile struct {
	Total           int
	Resolved        int
	FlailingRepeats int // sum of (RepeatCount-1) across error_kind=="tool_error" rows
	AvgResolveMins  float64
}

// ComputeErrorProfile builds an ErrorProfile from a project's failure signatures.
func ComputeErrorProfile(sigs []db.FailureSignature) ErrorProfile {
	var p ErrorProfile
	p.Total = len(sigs)

	var resolveMinsSum float64
	for _, sig := range sigs {
		if sig.ErrorKind == "tool_error" && sig.RepeatCount > 1 {
			p.FlailingRepeats += sig.RepeatCount - 1
		}
		if sig.ResolvedTS == "" {
			continue
		}
		first, err1 := time.Parse(time.RFC3339, sig.FirstSeenTS)
		resolved, err2 := time.Parse(time.RFC3339, sig.ResolvedTS)
		if err1 != nil || err2 != nil {
			continue
		}
		p.Resolved++
		resolveMinsSum += resolved.Sub(first).Minutes()
	}
	if p.Resolved > 0 {
		p.AvgResolveMins = resolveMinsSum / float64(p.Resolved)
	}
	return p
}

// VibeCoderScore returns 0-100: 100 means fully engineer-like behavior,
// 0 means fully vibe-coder. Blends prompt steering, test verification, and
// (when available) repeated-error flailing. A project with no failure
// history is scored on steering+verification alone — no evidence of errors
// is never treated as evidence of vibe-coding.
func VibeCoderScore(steeringRate, verifiedRate float64, errs ErrorProfile) float64 {
	clamp := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	steeringRate = clamp(steeringRate)
	verifiedRate = clamp(verifiedRate)

	if errs.Total == 0 {
		return 100 * (0.5*steeringRate + 0.5*verifiedRate)
	}

	flailRate := clamp(float64(errs.FlailingRepeats) / float64(errs.Total))
	understanding := 1 - flailRate

	return 100 * (0.35*steeringRate + 0.35*verifiedRate + 0.3*understanding)
}
