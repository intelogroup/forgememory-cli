// Package knowledgegap derives a builder's technical knowledge gaps and
// vocabulary mismatches from local forge signals — distilled cross-session
// patterns, corrective principles, repeated failure signatures, and raw
// prompt text — via a single LLM call grounded only in that evidence.
package knowledgegap

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/forge/forge/internal/db"
)

// Gap is one ranked technical knowledge gap with cited evidence.
type Gap struct {
	Topic         string `json:"topic"`
	Evidence      string `json:"evidence"`
	ConcreteLesson string `json:"concrete_lesson"`
	StudyPointer  string `json:"study_pointer"`
}

// VocabSuggestion flags an imprecise/wrong term the user used and the
// correct term for the concept they were actually reaching for.
type VocabSuggestion struct {
	TermUsed    string `json:"term_used"`
	CorrectTerm string `json:"correct_term"`
	Why         string `json:"why"`
}

// Result is the parsed two-section LLM response.
type Result struct {
	Gaps       []Gap             `json:"gaps"`
	Vocabulary []VocabSuggestion `json:"vocabulary"`
}

// FailureBucket is a repeated failure signature aggregated by kind+message
// prefix, mirroring how the raw data is actually recurring (not one row per
// occurrence — that's just noise for the LLM).
type FailureBucket struct {
	ErrorKind    string
	MessagePrefix string
	RepeatCount  int
}

// Signals is the local evidence digest fed to the LLM.
type Signals struct {
	ProjectID string

	KnowledgeGapPatterns []string // pattern text, pattern_type == "knowledge_gap"
	RecurringFailures    []string // pattern text, pattern_type == "recurring_failure"

	GotchaPrinciples []string // title + narrative, type == "gotcha"
	BugfixPrinciples []string // title + narrative, type == "bugfix"

	FailureBuckets []FailureBucket

	PromptSamples []string // prompts containing decision/question language
	VocabTerms    []string // raw technical tokens extracted from PromptSamples
}

const maxSamplesPerSection = 15

// decisionMarkers select prompts most likely to carry the user's own
// technical reasoning (as opposed to task-only prompts like "continue" or
// "fix this") — the same markers used to hand-mine this signal manually.
var decisionMarkers = []string{
	"instead of", " vs ", "shd we", "should we", "why is", "why does", "why do",
	"what is", "what does", "how does", "how do i", "explain", "difference between",
	"tradeoff", "trade-off", "rather than",
}

// FilterDecisionPrompts returns the subset of prompts containing decision or
// question language, most recent first, capped at maxSamplesPerSection.
func FilterDecisionPrompts(prompts []string) []string {
	var out []string
	for i := len(prompts) - 1; i >= 0 && len(out) < maxSamplesPerSection; i-- {
		lower := strings.ToLower(prompts[i])
		for _, m := range decisionMarkers {
			if strings.Contains(lower, m) {
				out = append(out, prompts[i])
				break
			}
		}
	}
	return out
}

var vocabToken = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_.]{2,}\b`)

// stopwords excludes common English words and forge/tool boilerplate terms
// that would otherwise dominate token frequency without being technical
// vocabulary worth correcting.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "that": true, "with": true,
	"this": true, "have": true, "are": true, "was": true, "can": true, "but": true,
	"not": true, "what": true, "how": true, "why": true, "shd": true, "its": true,
	"just": true, "like": true, "get": true, "use": true, "using": true, "now": true,
	"want": true, "need": true, "then": true, "than": true, "also": true, "make": true,
}

// ExtractVocabTerms tokenizes prompt text into candidate technical terms for
// the vocabulary-correction pass, deduplicated and stopword-filtered.
func ExtractVocabTerms(prompts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range prompts {
		for _, tok := range vocabToken.FindAllString(p, -1) {
			lower := strings.ToLower(tok)
			if stopwords[lower] || seen[lower] {
				continue
			}
			seen[lower] = true
			out = append(out, tok)
		}
	}
	return out
}

// BucketFailures aggregates raw failure signatures by (error_kind, message
// prefix) so recurring errors count once with a repeat total, instead of the
// LLM seeing the same fingerprint's rows individually.
func BucketFailures(sigs []db.FailureSignature) []FailureBucket {
	type key struct{ kind, prefix string }
	counts := map[key]int{}
	for _, s := range sigs {
		msg := s.NormalizedMessage
		if len(msg) > 80 {
			msg = msg[:80]
		}
		counts[key{s.ErrorKind, msg}] += s.RepeatCount
	}
	out := make([]FailureBucket, 0, len(counts))
	for k, n := range counts {
		out = append(out, FailureBucket{ErrorKind: k.kind, MessagePrefix: k.prefix, RepeatCount: n})
	}
	return out
}

// BuildPrompt renders the signals into an LLM prompt that produces ranked,
// evidence-cited knowledge gaps plus vocabulary corrections — grounded only
// in the evidence given, no generic advice.
func BuildPrompt(s Signals) string {
	return buildPrompt(s, false)
}

// BuildVocabOnlyPrompt renders a prompt that skips gap detection entirely —
// cheaper/faster when only the vocabulary-correction pass is wanted.
func BuildVocabOnlyPrompt(s Signals) string {
	return buildPrompt(s, true)
}

func buildPrompt(s Signals, vocabOnly bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are a principal engineer identifying a software builder's concrete technical knowledge gaps on project %q using ONLY the evidence below. Do not invent facts. Every gap must cite specific evidence; no generic advice like \"learn testing\" without naming what specifically recurred.\n\n", s.ProjectID)

	sb.WriteString("Evidence:\n")
	if len(s.KnowledgeGapPatterns) > 0 {
		sb.WriteString("- Distilled knowledge-gap patterns (from cross-session analysis):\n")
		for _, p := range cap15(s.KnowledgeGapPatterns) {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}
	if len(s.RecurringFailures) > 0 {
		sb.WriteString("- Recurring failure patterns:\n")
		for _, p := range cap15(s.RecurringFailures) {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}
	if len(s.GotchaPrinciples) > 0 {
		sb.WriteString("- Distilled gotcha lessons (forge's own highest-trust corrective signal):\n")
		for _, p := range cap15(s.GotchaPrinciples) {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}
	if len(s.BugfixPrinciples) > 0 {
		sb.WriteString("- Distilled bugfix lessons:\n")
		for _, p := range cap15(s.BugfixPrinciples) {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}
	if len(s.FailureBuckets) > 0 {
		sb.WriteString("- Repeated tool/error signatures (kind, message prefix, repeat count):\n")
		for _, b := range s.FailureBuckets {
			fmt.Fprintf(&sb, "  - [%s] %q x%d\n", b.ErrorKind, b.MessagePrefix, b.RepeatCount)
		}
	}
	if len(s.PromptSamples) > 0 {
		sb.WriteString("- User's own verbatim technical prompts/questions (judge reasoning quality and vocabulary directly from these):\n")
		for _, p := range s.PromptSamples {
			fmt.Fprintf(&sb, "  - %q\n", truncate(p, 240))
		}
	}
	if len(s.VocabTerms) > 0 {
		fmt.Fprintf(&sb, "- Raw technical terms extracted from those prompts (for the vocabulary section, evaluate ONLY terms actually misused given the surrounding context above — do not flag correctly-used jargon): %s\n",
			strings.Join(s.VocabTerms, ", "))
	}

	if vocabOnly {
		sb.WriteString(`
Produce ONLY the "vocabulary" section — leave "gaps" as an empty array, do
not populate it.

"vocabulary": terms the user used imprecisely or incorrectly for the concept
the evidence shows them reaching for, each with the correct term and why it
matters (what confusion or bug the imprecise term enables). Only include
genuine mismatches — do not flag jargon used correctly, and leave this
empty if no prompt shows an actual imprecise/incorrect usage.

Respond with ONLY this JSON shape, no prose, no markdown fences, no trailing
commas, no comments — strictly valid JSON:
{"gaps":[],"vocabulary":[{"term_used":"...","correct_term":"...","why":"..."}]}
`)
		return sb.String()
	}

	sb.WriteString(`
Produce two sections:

1. "gaps": ranked technical knowledge gaps, most-recurring/highest-impact
   first. Each must name a SPECIFIC technical topic (e.g. "broad exception
   catching", "missing network timeouts", "config precedence" — not
   "engineering practices" or "testing"), cite the evidence line(s) that
   show it, give one concrete lesson (a rule, not a platitude), and one
   specific thing to study (a doc, concept, or exercise).

2. "vocabulary": terms the user used imprecisely or incorrectly for the
   concept the evidence shows them reaching for, each with the correct term
   and why it matters (what confusion or bug the imprecise term enables).
   Only include genuine mismatches — do not flag jargon used correctly, and
   leave this empty if no prompt shows an actual imprecise/incorrect usage.

Respond with ONLY this JSON shape, no prose, no markdown fences, no trailing
commas, no comments — strictly valid JSON:
{"gaps":[{"topic":"...","evidence":"...","concrete_lesson":"...","study_pointer":"..."}],"vocabulary":[{"term_used":"...","correct_term":"...","why":"..."}]}
`)
	return sb.String()
}

func cap15(items []string) []string {
	if len(items) > maxSamplesPerSection {
		return items[:maxSamplesPerSection]
	}
	return items
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var jsonBlock = regexp.MustCompile(`(?s)\{.*\}`)

// ParseResult extracts the JSON result object from an LLM response,
// tolerating surrounding prose or markdown code fences.
func ParseResult(resp string) (Result, error) {
	m := jsonBlock.FindString(resp)
	if m == "" {
		return Result{}, fmt.Errorf("no JSON object found in response")
	}
	var r Result
	if err := json.Unmarshal([]byte(m), &r); err != nil {
		return Result{}, fmt.Errorf("parsing result JSON: %w", err)
	}
	return r, nil
}
