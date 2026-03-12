package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		homeDir string
		want    string
	}{
		// Absolute paths (homeDir irrelevant).
		{"absolute path", "/home/user", "", "/home/user"},
		{"absolute macOS", "/Users/john", "", "/Users/john"},
		{"root path", "/", "", "/"},

		// Tilde expansion with homeDir.
		{"tilde alone", "~", "/home/user", "/home/user"},
		{"tilde with slash", "~/", "/home/user", "/home/user"},
		{"tilde subdir", "~/projects", "/home/user", "/home/user/projects"},
		{"tilde nested", "~/projects/myapp", "/home/user", "/home/user/projects/myapp"},
		{"tilde trailing slash", "~/projects/", "/home/user", "/home/user/projects"},
		{"tilde double slashes", "~//projects", "/home/user", "/home/user/projects"},
		{"tilde dot component", "~/./projects", "/home/user", "/home/user/projects"},

		// Tilde rejected without homeDir.
		{"tilde no homeDir", "~", "", ""},
		{"tilde subdir no homeDir", "~/projects", "", ""},

		// Empty and whitespace.
		{"empty string", "", "", ""},
		{"whitespace only", "   ", "", ""},

		// Relative paths (rejected).
		{"relative path", "home/user", "", ""},
		{"dot-relative", "./foo", "", ""},
		{"bare name", "foo", "", ""},

		// Path traversal (rejected).
		{"traversal mid", "/home/../etc/passwd", "", ""},
		{"traversal end", "/home/user/..", "", ""},
		{"traversal only", "/..", "", ""},
		{"tilde traversal", "~/../etc/passwd", "/home/user", ""},

		// Control character stripping.
		{"control chars stripped", "/home/\x01user", "", "/home/user"},
		{"control chars empty", "\x01\x02\x03", "", ""},
		{"DEL stripped", "/home/\x7Fuser", "", "/home/user"},
		{"tilde control chars", "~/\x01projects", "/home/user", "/home/user/projects"},

		// Normalization.
		{"trailing slash", "/home/user/", "", "/home/user"},
		{"double slashes", "/home//user", "", "/home/user"},
		{"dot components", "/home/./user", "", "/home/user"},
		{"whitespace trimmed", "  /home/user  ", "", "/home/user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizePath(tt.input, tt.homeDir))
		})
	}
}
