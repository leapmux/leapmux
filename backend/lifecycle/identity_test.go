package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityUsesCanonicalModeAndDirectory(t *testing.T) {
	base := t.TempDir()
	deep := base
	for _, segment := range []string{"a", "long", "chain", "of", "directories", "below", "the", "home"} {
		deep = filepath.Join(deep, segment)
	}
	require.NoError(t, os.MkdirAll(deep, 0o700))

	canonical, err := CanonicalDir(deep)
	require.NoError(t, err)

	// A symlinked alias of the same directory yields the same identity.
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(deep, alias))
	throughAlias, err := CanonicalDir(alias)
	require.NoError(t, err)
	assert.Equal(t, canonical, throughAlias, "a symlinked alias canonicalizes to the directory it points at")

	// A missing tail keeps the identity stable before and after the
	// directory exists.
	future := filepath.Join(deep, "not-created-yet")
	beforeCreation, err := CanonicalDir(future)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(future, 0o700))
	afterCreation, err := CanonicalDir(future)
	require.NoError(t, err)
	assert.Equal(t, beforeCreation, afterCreation,
		"the identity of a not-yet-created directory does not change when it appears")

	// The same directory under two modes yields two instance identities.
	hub := NewInstanceID("501", RoleHub, canonical)
	worker := NewInstanceID("501", RoleWorker, canonical)
	assert.NotEqual(t, hub, worker, "the mode separates instances")

	// Two configurations that resolve to the same mode and directory
	// identify the same instance: the alias and the deep path contend.
	assert.Equal(t,
		NewInstanceID("501", RoleWorker, canonical),
		NewInstanceID("501", RoleWorker, throughAlias))
}

// ParseRole admits exactly the four mode names and refuses everything
// else, including the empty string.
func TestParseRole(t *testing.T) {
	for _, role := range []Role{RoleHub, RoleWorker, RoleSolo, RoleDev} {
		parsed, err := ParseRole(string(role))
		require.NoError(t, err)
		assert.Equal(t, role, parsed)
	}
	for _, bad := range []string{"", "Hub", "server", "workerd"} {
		_, err := ParseRole(bad)
		assert.Error(t, err, "mode %q", bad)
	}
}

// CanonicalDir expands a leading ~ the way the mode loaders do, so the
// CLI's default data directories name the same directory on disk.
func TestCanonicalDirExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	for input, want := range map[string]string{
		"~":                     home,
		"~/":                    home,
		"~/.config/leapmux/hub": filepath.Join(home, ".config", "leapmux", "hub"),
	} {
		got, err := CanonicalDir(input)
		require.NoError(t, err, "input %q", input)
		assert.Equal(t, want, got, "input %q", input)
	}

	// A ~ that is not the account's home marker stays literal: only a
	// lone ~ or a ~/ prefix identifies the home.
	got, err := CanonicalDir("~other/leapmux")
	require.NoError(t, err)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	cwd, err = filepath.EvalSymlinks(cwd)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cwd, "~other", "leapmux"), got)
}
