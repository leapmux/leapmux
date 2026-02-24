package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"single char", "a", "a", false, ""},
		{"single digit", "1", "1", false, ""},
		{"lowercase", "myname", "myname", false, ""},
		{"with numbers", "user123", "user123", false, ""},
		{"with hyphen", "my-name", "my-name", false, ""},
		{"multiple hyphens separated", "a-b-c", "a-b-c", false, ""},
		{"max length 32", strings.Repeat("a", 32), strings.Repeat("a", 32), false, ""},

		// Trimming and lowercasing
		{"uppercase lowercased", "MyName", "myname", false, ""},
		{"all uppercase", "HELLO", "hello", false, ""},
		{"leading spaces trimmed", "  hello", "hello", false, ""},
		{"trailing spaces trimmed", "hello  ", "hello", false, ""},
		{"both spaces trimmed", "  hello  ", "hello", false, ""},
		{"uppercase with hyphen", "My-Org-123", "my-org-123", false, ""},

		// Empty / length
		{"empty", "", "", true, "must not be empty"},
		{"whitespace only", "   ", "", true, "must not be empty"},
		{"too long 33", strings.Repeat("a", 33), "", true, "at most 32"},

		// Invalid characters
		{"space in middle", "my name", "", true, "only letters, numbers, and hyphens"},
		{"underscore", "my_name", "", true, "only letters, numbers, and hyphens"},
		{"dot", "my.name", "", true, "only letters, numbers, and hyphens"},
		{"at sign", "user@org", "", true, "only letters, numbers, and hyphens"},
		{"slash", "user/org", "", true, "only letters, numbers, and hyphens"},
		{"unicode", "caf\u00e9", "", true, "only letters, numbers, and hyphens"},
		{"emoji", "hello\U0001F600", "", true, "only letters, numbers, and hyphens"},

		// Structural: leading/trailing hyphen
		{"leading hyphen", "-myname", "", true, "must not start with a hyphen"},
		{"trailing hyphen", "myname-", "", true, "must not end with a hyphen"},
		{"only hyphen", "-", "", true, "must not start with a hyphen"},

		// Structural: consecutive hyphens
		{"consecutive hyphens", "my--name", "", true, "consecutive hyphens"},
		{"triple hyphens", "my---name", "", true, "consecutive hyphens"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeSlug("test field", tt.input)
			if tt.wantErr {
				require.Error(t, err, "SanitizeSlug(%q) should return error", tt.input)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Empty(t, got)
			} else {
				require.NoError(t, err, "SanitizeSlug(%q) should not return error", tt.input)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSanitizeSlug_FieldNameInError(t *testing.T) {
	_, err := SanitizeSlug("username", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "username")

	_, err = SanitizeSlug("organization name", "bad_slug")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "organization name")
}
