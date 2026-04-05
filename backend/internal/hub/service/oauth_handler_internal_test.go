package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeRedirectURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"empty", "", ""},
		{"root", "/", "/"},
		{"relative path", "/workspace/123", "/workspace/123"},
		{"deep path", "/a/b/c?q=1", "/a/b/c?q=1"},
		{"absolute URL rejected", "https://evil.com", ""},
		{"http URL rejected", "http://evil.com", ""},
		{"protocol-relative rejected", "//evil.com", ""},
		{"protocol-relative with path rejected", "//evil.com/callback", ""},
		{"bare domain rejected", "evil.com", ""},
		{"javascript scheme rejected", "javascript:alert(1)", ""},
		{"data scheme rejected", "data:text/html,<h1>hi</h1>", ""},
		{"backslash rejected", "\\evil.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeRedirectURI(tt.uri))
		})
	}
}
