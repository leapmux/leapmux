package canonicalpath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectoryResolvesAliasesAndMissingSuffixes(t *testing.T) {
	base := t.TempDir()
	real, err := filepath.EvalSymlinks(base)
	require.NoError(t, err)
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(base, alias))
	actual, err := Directory(filepath.Join(alias, "missing", "leaf"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(real, "missing", "leaf"), actual)
	assert.NoDirExists(t, filepath.Join(base, "missing"), "canonical resolution does not publish directories")
}

func TestDirectoryRejectsEmptyInputAndSymlinkLoops(t *testing.T) {
	_, err := Directory("")
	require.Error(t, err)
	loop := filepath.Join(t.TempDir(), "loop")
	require.NoError(t, os.Symlink(loop, loop))
	_, err = Directory(filepath.Join(loop, "child"))
	require.Error(t, err, "an unreadable prefix must not be treated as missing")
}
