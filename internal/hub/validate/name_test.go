package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "hello", false},
		{"valid with spaces", "hello world", false},
		{"valid with hyphens", "my-name", false},
		{"valid with underscores", "my_name", false},
		{"valid with dots", "my.name", false},
		{"valid with numbers", "name123", false},
		{"valid mixed", "My Name-1.0_beta", false},
		{"valid max length", string(make([]byte, 64)), true}, // 64 null bytes are invalid chars
		{"valid 64 chars", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true}, // 65 chars
		{"special chars @", "name@here", true},
		{"special chars !", "hello!", true},
		{"special chars /", "path/name", true},
		{"special chars \\", "back\\slash", true},
		{"special chars quotes", `name"quoted`, true},
		{"unicode", "caf\u00e9", true},
		{"emoji", "hello\U0001F600", true},
		{"leading spaces trimmed", "  hello  ", false},
		{"tabs", "hello\tworld", true},
		{"newline", "hello\nworld", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if tt.wantErr {
				assert.Error(t, err, "ValidateName(%q) should return error", tt.input)
			} else {
				assert.NoError(t, err, "ValidateName(%q) should not return error", tt.input)
			}
		})
	}
}
