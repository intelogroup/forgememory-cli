package sanitize

import (
	"regexp"
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

	// Authorization headers and other common credential strings
	bearerPattern   = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9-_=]+\.[A-Za-z0-9-_=]+\.?[A-Za-z0-9-_.+/=]*\b`)
	passwordPattern = regexp.MustCompile(`(?i)\b[a-z0-9_]*(password|pass|passwd|pwd|secret|api_key|apikey|token|private_key|privatekey)\b\s*([:=]|\s)\s*\\?["']?([A-Za-z0-9-_.+/=]{8,})["']?`)
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
