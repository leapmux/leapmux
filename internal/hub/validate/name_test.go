package validate

import (
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
			{"special chars %", "100%", "100%"},
			{"unicode", "café", "café"},
			{"emoji", "hello\U0001F600", "hello\U0001F600"},
			{"64 chars", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{"trims leading/trailing spaces", "  hello  ", "hello"},
			// Forbidden chars are silently stripped
			{"strips double quotes", `name"quoted`, "namequoted"},
			{"strips backslashes", "back\\slash", "backslash"},
			{"strips tabs", "hello\tworld", "helloworld"},
			{"strips newlines", "hello\nworld", "helloworld"},
			{"strips control chars", "hello\x00world", "helloworld"},
			{"strips 0x1F", "hello\x1Fworld", "helloworld"},
			{"strips 0x7F", "hello\x7Fworld", "helloworld"},
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
			{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, // 65 chars
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
