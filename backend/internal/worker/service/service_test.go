package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "failed to get home dir")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/Documents", filepath.Join(home, "Documents")},
		{"tilde nested", "~/a/b/c", filepath.Join(home, "a/b/c")},
		{"absolute path unchanged", "/usr/local/bin", "/usr/local/bin"},
		{"relative path unchanged", "some/path", "some/path"},
		{"empty string", "", ""},
		{"double tilde unchanged", "~~", "~~"},
		{"tilde in middle unchanged", "/foo/~/bar", "/foo/~/bar"},
		{"tilde user unchanged", "~user/foo", "~user/foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTilde(tt.in)
			assert.Equal(t, tt.want, got, "expandTilde(%q)", tt.in)
		})
	}
}
