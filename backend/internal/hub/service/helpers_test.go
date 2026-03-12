package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateBranchName(t *testing.T) {
	validNames := []string{
		"feature-branch",
		"fix/login-bug",
		"v1.0.0",
		"my_branch",
		"a",
		"feature/deep/nesting",
		"UPPERCASE",
		"mixed-Case_123",
		"release/2024.01",
		strings.Repeat("a", 256), // max length
	}

	for _, name := range validNames {
		t.Run("valid: "+name[:min(len(name), 30)], func(t *testing.T) {
			assert.NoError(t, ValidateBranchName(name))
		})
	}

	invalidTests := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{"empty", "", "must not be empty"},
		{"too long", strings.Repeat("a", 257), "at most 256"},
		{"space", "foo bar", "must not contain ' '"},
		{"tilde", "foo~bar", "must not contain '~'"},
		{"caret", "foo^bar", "must not contain '^'"},
		{"colon", "foo:bar", "must not contain ':'"},
		{"question", "foo?bar", "must not contain '?'"},
		{"asterisk", "foo*bar", "must not contain '*'"},
		{"open bracket", "foo[bar", "must not contain '['"},
		{"close bracket", "foo]bar", "must not contain ']'"},
		{"backslash", "foo\\bar", "must not contain '\\'"},
		{"null byte", "foo\x00bar", "control characters"},
		{"newline", "foo\nbar", "control characters"},
		{"tab", "foo\tbar", "control characters"},
		{"DEL", "foo\x7fbar", "control characters"},
		{"leading dot", ".foo", "must not start with '.'"},
		{"leading dash", "-foo", "must not start with '-'"},
		{"leading slash", "/foo", "must not start with '/'"},
		{"leading @", "@foo", "must not start with '@'"},
		{"trailing slash", "foo/", "must not end with"},
		{"trailing dot", "foo.", "must not end with"},
		{"trailing .lock", "foo.lock", "must not end with"},
		{"double dot", "foo..bar", "must not contain '..'"},
		{"double slash", "foo//bar", "must not contain '//'"},
		{"slash-dot", "foo/.bar", "must not contain '/.'"},
	}

	for _, tt := range invalidTests {
		t.Run("invalid: "+tt.name, func(t *testing.T) {
			err := ValidateBranchName(tt.input)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}
