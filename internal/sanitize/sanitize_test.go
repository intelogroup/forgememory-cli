package sanitize

import (
	"strings"
	"testing"
)

func TestScrubSecrets(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "OpenAI key: sk-U34JFd9823k4j8sdfjlkjsdfU89sdfjklsd",
			expected: "OpenAI key: [REDACTED-OPENAI-KEY]",
		},
		{
			input:    "Anthropic key: sk-ant-sid01-1234567890abcdef-1234567890abcdef-1234567890abcdef-1234567890abcdef",
			expected: "Anthropic key: [REDACTED-ANTHROPIC-KEY]",
		},
		{
			input:    "Authorization: bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			expected: "Authorization: Bearer [REDACTED-BEARER-TOKEN]",
		},
		{
			input:    "db_password = mySuperSecretPassword123",
			expected: "db_password = [REDACTED-SECRET]",
		},
		{
			input:    "api_key:\"some_long_key_string\"",
			expected: "api_key:\"[REDACTED-SECRET]\"",
		},
		{
			input:    "regular text without secrets",
			expected: "regular text without secrets",
		},
	}

	for _, test := range tests {
		result := ScrubSecrets(test.input)
		if result != test.expected {
			t.Errorf("ScrubSecrets(%q) = %q; expected %q", test.input, result, test.expected)
		}
	}
}

// Fake credentials are assembled from pieces so secret scanners ignore this file.
func TestScrubSecretsCoversProviderFormats(t *testing.T) {
	body := strings.Repeat("A1b2C3d4", 5)
	tests := []struct {
		name, input, expected string
	}{
		{"groq underscore", "key gsk_" + body + " end", "key [REDACTED-GROQ-KEY] end"},
		{"groq hyphen", "key gsk-" + body + " end", "key [REDACTED-GROQ-KEY] end"},
		{"stripe live secret", "STRIPE=sk_" + "live_" + body, "STRIPE=[REDACTED-STRIPE-KEY]"},
		{"stripe restricted", "rk_" + "live_" + body, "[REDACTED-STRIPE-KEY]"},
		{"stripe webhook", "whsec_" + body, "[REDACTED-STRIPE-KEY]"},
		{"aws access key id", "id AKIA" + "ABCDEFGHIJKLMNOP end", "id [REDACTED-AWS-KEY-ID] end"},
		{"github pat", "tok ghp_" + body + " end", "tok [REDACTED-GITHUB-TOKEN] end"},
		{"bare jwt", "SUPABASE=eyJhbGciOiJIUzI1NiJ9.eyJyb2xlIjoic2VydmljZV9yb2xlIn0.c2lnbmF0dXJlMTIzNDU2", "SUPABASE=[REDACTED-JWT]"},
		{"url password", "postgresql://user:p4ssw0rdVALUE99@host:5432/db", "postgresql://user:[REDACTED-URL-PASSWORD]@host:5432/db"},
		{"escaped json value", `{"cmd":"export TOKEN=\"abcdEFGH12345678abcdEFGH\""}`, `{"cmd":"export TOKEN=\"[REDACTED-SECRET]\""}`},
		{"url without password", "https://example.com:8080/path", "https://example.com:8080/path"},
		{"scp style git", "git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"url with user only", "https://user@example.com/x", "https://user@example.com/x"},
		{"short eyJ", "eyJ is a prefix", "eyJ is a prefix"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScrubSecrets(tc.input); got != tc.expected {
				t.Errorf("ScrubSecrets(%q) = %q; expected %q", tc.input, got, tc.expected)
			}
		})
	}
}
