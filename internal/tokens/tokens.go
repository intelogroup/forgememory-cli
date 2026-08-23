package tokens

import (
	"sort"
	"strings"
	"unicode"
)

// CommonStopWords is the shared stop-word set used for prompt recall and
// distillation query tokenization. Kept in one place so cmd/hook,
// internal/distill and internal/mcp do not diverge.
var CommonStopWords = map[string]bool{
	"a": true, "about": true, "after": true, "agent": true, "all": true,
	"also": true, "and": true, "are": true, "been": true, "before": true,
	"build": true, "change": true, "claude": true, "codex": true, "create": true,
	"feature": true, "for": true, "from": true, "gemini": true, "have": true,
	"implement": true, "into": true, "just": true, "make": true, "need": true,
	"next": true, "prompt": true, "project": true, "repo": true, "same": true,
	"session": true, "some": true, "that": true, "the": true, "their": true,
	"there": true, "they": true, "this": true, "tool": true, "user": true,
	"using": true, "want": true, "with": true, "work": true, "working": true,
}

// TokenSet splits text into lower-cased alphanumeric tokens, drops tokens
// shorter than 3 chars and any entry in stopWords (nil = no stop-word filter).
func TokenSet(text string, stopWords map[string]bool) map[string]bool {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool, len(parts))
	for _, part := range parts {
		if len(part) < 3 {
			continue
		}
		if stopWords != nil && stopWords[part] {
			continue
		}
		set[part] = true
	}
	return set
}

// SortedKeys returns the keys of set sorted alphabetically, capped at 12.
// Matches the previous recallTokens/tokenize behavior (cap + sort).
func SortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 12 {
		keys = keys[:12]
	}
	return keys
}

// Tokenize is TokenSet + SortedKeys in one call.
func Tokenize(text string, stopWords map[string]bool) []string {
	return SortedKeys(TokenSet(text, stopWords))
}
