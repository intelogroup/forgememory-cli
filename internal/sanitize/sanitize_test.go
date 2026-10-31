package sanitize

import (
	"encoding/json"
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

func TestScrubSecretsCoversBareProviderTokens(t *testing.T) {
	alnum := strings.Repeat("a1B2c3D4e5", 4)
	for name, token := range map[string]string{
		"supabase access": "sbp_" + strings.Repeat("0a1b2c3d", 5),
		"supabase secret": "sb_secret_" + alnum,
		"neon password":   "npg_" + alnum[:14],
		"neon api":        "napi_" + alnum + alnum,
		"slack":           "xoxb-" + "123456789012-" + alnum,
		"google":          "AIza" + alnum[:35],
		"hugging face":    "hf_" + alnum + alnum,
		"npm":             "npm_" + alnum[:36],
		"resend":          "re_" + alnum[:8] + "_" + alnum,
		"fly":             "fm2_" + alnum + alnum,
		"telegram":        "1234567890:" + alnum[:35],
	} {
		// No credential word nearby: the token alone must be enough.
		got := ScrubSecrets(`{"value":"` + token + `","n":1}`)
		if strings.Contains(got, token) {
			t.Errorf("%s survived: %s", name, got)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("%s broke the JSON: %s", name, got)
		}
	}
}

func TestScrubSecretsCatchesPasswordsInProseAndJSON(t *testing.T) {
	const value = "hunter2Fake0"
	for _, input := range []string{
		"my staging database password is " + value,
		"The API key is " + value + ".",
		"the token was " + value,
		"mon mot de passe est " + value,
		"password: 'P@ssw0rd!23'",
		"password=P@ssw0rd!23",
		"SECRET_KEY=" + value + "99",
		"AWS_SECRET_ACCESS_KEY=" + value + "99",
		`{"password":"` + value + `"}`,
		`{\"password\": \"` + value + `\"}`,
		`{\\\"client_secret\\\":\\\"` + value + `\\\"}`,
	} {
		got := ScrubSecrets(input)
		if strings.Contains(got, value) || strings.Contains(got, "ssw0rd") {
			t.Errorf("secret survived %q: %q", input, got)
		}
		if !strings.Contains(got, "[REDACTED-SECRET]") {
			t.Errorf("no redaction marker in %q", got)
		}
	}
}

func TestScrubSecretsLeavesOrdinaryProseAlone(t *testing.T) {
	for _, input := range []string{
		"the password is required",
		"the token is expired.",
		"secret is stored in keychain",
		"the token is read-only",
		"the token is v2",
		"api key is in .env",
		"password reset flow",
		`{"input_tokens":1234567890,"total_tokens":1234567890}`,
		"disk-usage-report-for-the-whole-server-rack",
		"/tmp/task-0123456789abcdefghijklmnop/out",
	} {
		if got := ScrubSecrets(input); got != input {
			t.Errorf("over-redacted %q: %q", input, got)
		}
	}
}

func TestScrubSecretsIsIdempotentAndKeepsJSONValid(t *testing.T) {
	input := `{"password":"hunter2Fake0","prompt":"my token is abc123xyz and sbp_` + strings.Repeat("0a1b2c3d", 5) + `","cwd":"/x"}`
	once := ScrubSecrets(input)
	if !json.Valid([]byte(once)) {
		t.Fatalf("invalid JSON: %s", once)
	}
	if twice := ScrubSecrets(once); twice != once {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
}
