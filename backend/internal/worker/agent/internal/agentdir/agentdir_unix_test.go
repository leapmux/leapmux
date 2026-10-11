//go:build unix

package agentdir

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parent must be a directory of the user. A link could point anywhere, so
// New takes the next base, and the sweep does not follow the link.
func TestNewTakesTheNextBaseWhenTheParentIsALink(t *testing.T) {
	t.Parallel()
	linked, open := newBase(t), newBase(t)
	target := newBase(t)
	elsewhere := staleDir(t, target, testSpec, 1, true)
	require.NoError(t, os.Symlink(parentOf(target), parentOf(linked)))

	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{linked, open}})
	dir := newDir(t, dirs, testSpec)
	assert.Equal(t, parentOf(open), filepath.Dir(dir.Path()))
	assert.DirExists(t, elsewhere, "the sweep does not follow a linked parent")

	only := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{linked}})
	_, err := only.New(context.Background(), testSpec)
	require.ErrorContains(t, err, "not a directory")
}

// A parent of the user that other users can reach becomes private.
func TestNewMakesTheParentPrivate(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	require.NoError(t, os.Mkdir(parentOf(base), 0o755))
	require.NoError(t, os.Chmod(parentOf(base), 0o755))
	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	newDir(t, dirs, testSpec)
	info, err := os.Stat(parentOf(base))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// A link in place of the lock file is not a lock that a worker holds. The
// sweep leaves such a directory alone, and never opens the link's target.
func TestTheSweepLeavesADirectoryWhoseLockFileIsALink(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	dir := staleDir(t, base, testSpec, 1, false)
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.Symlink(target, filepath.Join(dir, lockFileName)))
	startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	assert.DirExists(t, dir)
	assert.NoFileExists(t, target, "the sweep created nothing at the target of the link")
}

func TestUsableBasesDropsALinkToAnEarlierBase(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	assert.Equal(t, []string{real}, usableBases([]string{real, link}))
}

func TestFileOwnerIsTheUserOfThisProcess(t *testing.T) {
	t.Parallel()
	info, err := os.Stat(t.TempDir())
	require.NoError(t, err)
	owner, ok := FileOwner(info)
	require.True(t, ok)
	assert.Equal(t, os.Getuid(), owner)
}
