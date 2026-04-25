package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeProperty(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "hello", "hello"},
		{"with hyphens", "my-host", "my-host"},
		{"with underscores", "my_host", "my_host"},
		{"with dots", "host.local", "host.local"},
		{"with numbers", "host123", "host123"},
		{"mixed valid", "My-Host_1.0", "My-Host_1.0"},
		{"spaces removed", "my host", "myhost"},
		{"special chars removed", "host@name!", "hostname"},
		{"slashes removed", "path/to/thing", "pathtothing"},
		{"unicode removed", "caf\u00e9", "caf"},
		{"all invalid", "@#$%^&*()", ""},
		{"empty", "", ""},
		{"tabs removed", "hello\tworld", "helloworld"},
		{"newlines removed", "hello\nworld", "helloworld"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, SanitizeProperty(tt.input))
		})
	}
}

func TestValidateProperty(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		input   string
		wantVal string
		wantErr bool
	}{
		{"valid simple", "hostname", "my-host", "my-host", false},
		{"sanitized with spaces", "hostname", "my host!", "myhost", false},
		{"sanitized with slashes", "os", "linux/amd64", "linuxamd64", false},
		{"all invalid", "hostname", "@#$%", "", true},
		{"empty input", "os", "", "", true},
		{"whitespace only", "arch", "   ", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := ValidateProperty(tt.field, tt.input)
			if tt.wantErr {
				assert.Error(t, err, "ValidateProperty(%q, %q) should return error", tt.field, tt.input)
				assert.Contains(t, err.Error(), tt.field)
			} else {
				assert.NoError(t, err, "ValidateProperty(%q, %q) should not return error", tt.field, tt.input)
				assert.Equal(t, tt.wantVal, val)
			}
		})
	}
}
