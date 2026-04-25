package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeName(t *testing.T) {
	t.Run("returns sanitized name", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
			want  string
		}{
			{"simple", "hello", "hello"},
			{"with spaces", "hello world", "hello world"},
			{"with hyphens", "my-name", "my-name"},
			{"with underscores", "my_name", "my_name"},
			{"with dots", "my.name", "my.name"},
			{"with numbers", "name123", "name123"},
			{"mixed", "My Name-1.0_beta", "My Name-1.0_beta"},
			{"special chars @", "name@here", "name@here"},
			{"special chars !", "hello!", "hello!"},
			{"special chars /", "path/name", "path/name"},
			{"special chars '", "it's fine", "it's fine"},
			{"special chars +", "a + b = c", "a + b = c"},
			{"special chars parens", "project (draft)", "project (draft)"},
			{"unicode", "café", "café"},
			{"emoji", "hello\U0001F600", "hello\U0001F600"},
			{"128 chars", strings.Repeat("a", 128), strings.Repeat("a", 128)},
			{"trims leading/trailing spaces", "  hello  ", "hello"},
			// Forbidden chars are silently stripped
			{"strips double quotes", `name"quoted`, "namequoted"},
			{"strips backslashes", "back\\slash", "backslash"},
			{"strips tabs", "hello\tworld", "helloworld"},
			{"strips newlines", "hello\nworld", "helloworld"},
			{"strips control chars", "hello\x00world", "helloworld"},
			{"strips 0x1F", "hello\x1Fworld", "helloworld"},
			{"strips 0x7F", "hello\x7Fworld", "helloworld"},
			{"strips dollar", "hello$world", "helloworld"},
			{"strips percent", "100%done", "100done"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := SanitizeName(tt.input)
				require.NoError(t, err, "SanitizeName(%q) should not return error", tt.input)
				assert.Equal(t, tt.want, got, "SanitizeName(%q) sanitized result", tt.input)
			})
		}
	})

	t.Run("returns error", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
		}{
			{"empty", ""},
			{"whitespace only", "   "},
			{"too long", strings.Repeat("a", 129)},
			{"only forbidden chars", string(make([]byte, 64))},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := SanitizeName(tt.input)
				assert.Error(t, err, "SanitizeName(%q) should return error", tt.input)
			})
		}
	})
}

func TestValidateSessionID(t *testing.T) {
	t.Run("accepts valid", func(t *testing.T) {
		assert.NoError(t, ValidateSessionID(""))
		assert.NoError(t, ValidateSessionID("abc-123"))
		assert.NoError(t, ValidateSessionID("session_456"))
		assert.NoError(t, ValidateSessionID("thread-uuid-v4-compat"))
	})

	t.Run("rejects invalid", func(t *testing.T) {
		assert.Error(t, ValidateSessionID("has\"quote"))
		assert.Error(t, ValidateSessionID("has\\backslash"))
		assert.Error(t, ValidateSessionID("has$dollar"))
		assert.Error(t, ValidateSessionID("has%percent"))
		assert.Error(t, ValidateSessionID("has\ttab"))
		assert.Error(t, ValidateSessionID(strings.Repeat("a", 129)))
	})

	t.Run("rejects control characters", func(t *testing.T) {
		// SanitizeName silently strips control chars; ValidateSessionID
		// must reject them because a session ID is an opaque token whose
		// original bytes matter, so silent mutation would confuse the
		// caller (they'd get back a different token than they sent).
		cases := []string{
			"has\x00nul",
			"has\x01soh",
			"has\x1Funitsep",
			"has\x7Fdel",
			"has\nnewline",
			"has\rcarriage",
		}
		for _, id := range cases {
			assert.Errorf(t, ValidateSessionID(id), "expected rejection for %q", id)
		}
	})
}
