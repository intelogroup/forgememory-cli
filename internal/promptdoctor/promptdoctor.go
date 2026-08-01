// Package promptdoctor detects recurring prompt anti-patterns (rephrase
// loops, degenerate one-word follow-ups, vague verbs with no artifact/
// condition) from a user's own UserPromptSubmit correction chains, then
// asks an LLM to propose SCARF-shaped fixes grounded only in that evidence.
package promptdoctor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Prompt is one submitted prompt with its timestamp, for chain grouping.
type Prompt struct {
	Text string
	TS   time.Time
}

// Chain is a run of prompts in the same session spaced under chainGap apart
// — a "correction chain" where the user kept re-prompting without the
// agent finishing in between.
type Chain struct {
	SessionID string
	Prompts   []Prompt
}

// Finding is one detected anti-pattern instance within a chain.
type Finding struct {
	ChainID      string
	AntiPattern  string // "rephrase_loop" | "degenerate" | "vague_verb"
	Prompt       string
}

// Signals is the local evidence digest fed to the LLM.
type Signals struct {
	ProjectID string
	Findings  []Finding
	SteeredRate float64 // fraction of prompts that interrupted an in-progress turn
	TotalPrompts int
}

// FixSuggestion is one LLM-proposed rewrite for a flagged prompt.
type FixSuggestion struct {
	ChainID           string `json:"chain_id"`
	AntiPattern       string `json:"anti_pattern"`
	Severity          string `json:"severity"`
	OriginalPrompt    string `json:"original_prompt"`
	FixedPrompt       string `json:"fixed_prompt"`
	MissingSCARFField string `json:"missing_scarf_field"`
}

// Result is the parsed LLM response: a ranked list of fix suggestions.
type Result struct {
	Fixes []FixSuggestion
}

const chainGap = 90 * time.Second

// GroupChains splits a session's chronologically-ordered prompts into
// chains, starting a new chain whenever the gap since the previous prompt
// is >= chainGap.
func GroupChains(sessionID string, prompts []Prompt) []Chain {
	var chains []Chain
	var cur Chain
	for i, p := range prompts {
		if i == 0 || p.TS.Sub(prompts[i-1].TS) >= chainGap {
			if len(cur.Prompts) > 0 {
				chains = append(chains, cur)
			}
			cur = Chain{SessionID: sessionID}
		}
		cur.Prompts = append(cur.Prompts, p)
	}
	if len(cur.Prompts) > 0 {
		chains = append(chains, cur)
	}
	return chains
}

var degenerateRe = regexp.MustCompile(`^(yes|no|next|fix|plan|debug|all|continue|go|ok|okay)\.?$`)

// isDegenerate flags one-word or fixed-phrase follow-ups that carry no new
// instruction content.
func isDegenerate(text string) bool {
	tokens := strings.Fields(text)
	if len(tokens) < 3 {
		return true
	}
	return degenerateRe.MatchString(strings.ToLower(strings.TrimSpace(text)))
}

var artifactPathRe = regexp.MustCompile(`\b[\w./-]+\.[a-z]{2,4}\b`)
var conditionRe = regexp.MustCompile(`(?i)\bpass\s*=|expect|should return`)
var vagueVerbRe = regexp.MustCompile(`(?i)\b(verify|check)\b`)

// isVagueVerb flags prompts that ask to "verify"/"check" without naming a
// concrete artifact path or a pass/fail condition.
func isVagueVerb(text string) bool {
	if !vagueVerbRe.MatchString(text) {
		return false
	}
	return !artifactPathRe.MatchString(text) && !conditionRe.MatchString(text)
}

// levenshteinRatio returns 1 - (edit distance / max length), so higher
// means more similar. Small hand-rolled implementation — no existing
// helper in the repo, not worth a dependency for ~10 lines.
func levenshteinRatio(a, b string) float64 {
	if a == b {
		return 1
	}
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	dist := prev[lb]
	max := la
	if lb > max {
		max = lb
	}
	return 1 - float64(dist)/float64(max)
}

const rephraseThreshold = 0.6

// DetectFindings scans one chain's consecutive prompt pairs for
// rephrase-loops (high textual similarity — the user restating the same
// ask), then classifies degenerate/vague-verb prompts individually.
func DetectFindings(chainID string, c Chain) []Finding {
	var out []Finding
	for i, p := range c.Prompts {
		if i > 0 {
			if levenshteinRatio(strings.ToLower(c.Prompts[i-1].Text), strings.ToLower(p.Text)) > rephraseThreshold {
				out = append(out, Finding{ChainID: chainID, AntiPattern: "rephrase_loop", Prompt: p.Text})
				continue
			}
		}
		if isDegenerate(p.Text) {
			out = append(out, Finding{ChainID: chainID, AntiPattern: "degenerate", Prompt: p.Text})
			continue
		}
		if isVagueVerb(p.Text) {
			out = append(out, Finding{ChainID: chainID, AntiPattern: "vague_verb", Prompt: p.Text})
		}
	}
	return out
}

// BuildPrompt renders the signals into an LLM prompt that produces
// SCARF-shaped fix suggestions grounded only in the evidence given.
func BuildPrompt(s Signals) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are a prompt-engineering coach reviewing a builder's own prompt history on project %q. Using ONLY the flagged prompts below, propose a concrete fixed rewrite for each — do not invent context not present in the original prompt.\n\n", s.ProjectID)

	fmt.Fprintf(&sb, "Steering rate (prompts that interrupted an in-progress agent turn): %.0f%% of %d prompts.\n\n", s.SteeredRate*100, s.TotalPrompts)

	sb.WriteString("Flagged prompts (chain_id, anti_pattern, original_prompt):\n")
	for _, f := range s.Findings {
		fmt.Fprintf(&sb, "  - [%s] %s: %q\n", f.ChainID, f.AntiPattern, truncate(f.Prompt, 240))
	}

	sb.WriteString(`
SCARF is: Scope (what to change), Condition (pass/fail criteria), Artifact
(file/path/function named), Report (what output to show), Fence (what NOT
to touch). Each fix should name the ONE SCARF field the original prompt was
missing.

Respond with ONLY a JSON array, no prose, no markdown fences, no trailing
commas, no comments — strictly valid JSON:
[{"chain_id":"...","anti_pattern":"...","severity":"low|medium|high","original_prompt":"...","fixed_prompt":"...","missing_scarf_field":"scope|condition|artifact|report|fence"}]
`)
	return sb.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var jsonArray = regexp.MustCompile(`(?s)\[.*\]`)

// ParseResult extracts the JSON array of fix suggestions from an LLM
// response, tolerating surrounding prose or markdown code fences.
func ParseResult(resp string) (Result, error) {
	m := jsonArray.FindString(resp)
	if m == "" {
		return Result{}, fmt.Errorf("no JSON array found in response")
	}
	var fixes []FixSuggestion
	if err := json.Unmarshal([]byte(m), &fixes); err != nil {
		return Result{}, fmt.Errorf("parsing result JSON: %w", err)
	}
	return Result{Fixes: fixes}, nil
}
