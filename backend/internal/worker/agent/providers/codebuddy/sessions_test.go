package codebuddy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodebuddyProjectSlugMatchesTheNativeProjectDirectory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/Users/trustin/Workspaces/leapmux", want: "Users-trustin-Workspaces-leapmux"},
		{path: `C:\Users\trustin\repo`, want: "C-Users-trustin-repo"},
		{path: "/tmp//nested///repo", want: "tmp-nested-repo"},
		{path: "/tmp/a:b/repo", want: "tmp-a-b-repo"},
		{path: "/tmp/a--b/repo", want: "tmp-a-b-repo"},
		{path: "/" + strings.Repeat("a", 260), want: strings.Repeat("a", 180) + "-1sqpx"},
		{path: "/" + strings.Repeat("한", 90), want: strings.Repeat("한", 60) + "-1n178g5"},
	} {
		assert.Equal(t, tc.want, codebuddyProjectSlug(tc.path), tc.path)
	}
}

func TestCodebuddyProjectSlugUsesTheResolvedWorkingDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	require.NoError(t, os.Mkdir(realDir, 0o755))
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("the test filesystem cannot create a symlink: %v", err)
	}
	assert.Equal(t, codebuddyProjectSlug(realDir), codebuddyProjectSlug(alias))
}
