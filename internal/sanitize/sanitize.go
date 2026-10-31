package sanitize

import (
	"regexp"
	"strings"
)

var (
	// Commonly known key/token patterns
	// Run Anthropic first so sk-ant- prefix is consumed before the general sk- pattern.
	anthropicPattern = regexp.MustCompile(`(?i)\bsk-ant-[a-zA-Z0-9_-]{20,}\b`)
	openAIPattern    = regexp.MustCompile(`(?i)\bsk-[a-zA-Z0-9_-]{20,}\b`)
	groqPattern      = regexp.MustCompile(`(?i)\bgsk[-_][a-zA-Z0-9_-]{20,}\b`)
	nvidiaPattern    = regexp.MustCompile(`(?i)\bnvapi-[a-zA-Z0-9_-]{20,}\b`)
	stripePattern    = regexp.MustCompile(`\b(?:[sr]k_(?:live|test)|whsec)_[A-Za-z0-9]{16,}\b`)
	awsKeyIDPattern  = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	githubPattern    = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})\b`)
	jwtPattern       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)
	// scheme://user:password@host — keep the scheme and user, drop the password.
	urlPasswordPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@"']+):[^\s@/"']{3,}@`)
	// Tokens recognizable on their own, with no credential word next to them.
	bareTokenPattern = regexp.MustCompile(`\b(?:sbp_[A-Za-z0-9]{20,}|sb_secret_[A-Za-z0-9_-]{16,}|npg_[A-Za-z0-9]{12,}|napi_[A-Za-z0-9]{32,}|xox[abeoprs]-[A-Za-z0-9-]{10,}|AIza[A-Za-z0-9_-]{35}|hf_[A-Za-z0-9]{30,}|npm_[A-Za-z0-9]{36}|re_[A-Za-z0-9]{8}_[A-Za-z0-9]{16,}|fm[12]_[A-Za-z0-9_+/=-]{20,}|[0-9]{8,10}:[A-Za-z0-9_-]{35})\b`)

	// Authorization headers and other common credential strings
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9-_=]+\.[A-Za-z0-9-_=]+\.?[A-Za-z0-9-_.+/=]*\b`)
	// The key may be quoted, at any JSON escaping depth: payloads nest tool input as strings.
	// A backslash only counts before a quote; alone it may start an escape like \\n.
	passwordPattern = regexp.MustCompile(`(?i)\b[a-z0-9_]*(password|pass|passwd|pwd|secret|secret_key|access_key|api_key|apikey|token|private_key|privatekey)\b(?:\\*["'])?\s*([:=]|\s)\s*(?:\\*["'])?([A-Za-z0-9-_.+/=!@#$%^&*~]{8,})["']?`)
	// "my password is X". shortcut: a letters-only value is left alone so "the token is expired" survives, upgrade if real passphrases leak.
	prosePasswordPattern = regexp.MustCompile("(?i)\\b(?:password|passwd|passphrase|passcode|pwd|mot de passe|api[ _-]?key|secret|token|credentials?)\\s+(?:is|was|est)\\s+(?:\\\\*[\"'`])?([^\\s\"'`\\\\,;]{6,})")
)

// ScrubSecrets redacts sensitive credentials and API keys from event payloads.
func ScrubSecrets(payload string) string {
	if payload == "" {
		return ""
	}

	payload = anthropicPattern.ReplaceAllString(payload, "[REDACTED-ANTHROPIC-KEY]")
	payload = openAIPattern.ReplaceAllString(payload, "[REDACTED-OPENAI-KEY]")
	payload = groqPattern.ReplaceAllString(payload, "[REDACTED-GROQ-KEY]")
	payload = nvidiaPattern.ReplaceAllString(payload, "[REDACTED-NVIDIA-KEY]")
	payload = stripePattern.ReplaceAllString(payload, "[REDACTED-STRIPE-KEY]")
	payload = awsKeyIDPattern.ReplaceAllString(payload, "[REDACTED-AWS-KEY-ID]")
	payload = githubPattern.ReplaceAllString(payload, "[REDACTED-GITHUB-TOKEN]")
	payload = bearerPattern.ReplaceAllString(payload, "Bearer [REDACTED-BEARER-TOKEN]")
	payload = jwtPattern.ReplaceAllString(payload, "[REDACTED-JWT]")
	payload = urlPasswordPattern.ReplaceAllString(payload, "$1:[REDACTED-URL-PASSWORD]@")
	payload = bareTokenPattern.ReplaceAllString(payload, "[REDACTED-API-KEY]")
	payload = prosePasswordPattern.ReplaceAllStringFunc(payload, func(match string) string {
		val := prosePasswordPattern.FindStringSubmatch(match)[1]
		if !strings.ContainsAny(val, "0123456789@#$%^&*!~") {
			return match
		}
		return strings.TrimSuffix(match, val) + "[REDACTED-SECRET]"
	})

	// For password/api_key matches, redact the value part while keeping the key prefix
	payload = passwordPattern.ReplaceAllStringFunc(payload, func(match string) string {
		submatches := passwordPattern.FindStringSubmatch(match)
		if len(submatches) >= 4 {
			// submatches[0] is the whole match
			// Find where the value begins in the whole match and replace it
			val := submatches[3]
			// Find index of value in match
			idx := regexp.MustCompile(regexp.QuoteMeta(val)).FindStringIndex(match)
			if idx != nil {
				return match[:idx[0]] + "[REDACTED-SECRET]" + match[idx[1]:]
			}
		}
		return "[REDACTED-SECRET]"
	})

	return payload
}
