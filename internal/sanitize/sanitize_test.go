package sanitize

import (
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
