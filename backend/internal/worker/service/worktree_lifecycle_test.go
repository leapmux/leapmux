package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeLifecycleAdmissionProtectsAncestorsAndAliases(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(nested, alias))
	var lifecycle WorktreeLifecycle
	release, err := lifecycle.admit(nested, alias)
	require.NoError(t, err)
	lock := lifecycle.directoryLock(pathutil.Canonicalize(root))
	if lock.TryLock() {
		lock.Unlock()
		t.Error("an admitted nested tab must prevent removal of its ancestor")
	}
	other := lifecycle.directoryLock(pathutil.Canonicalize(t.TempDir()))
	require.True(t, other.TryLock(), "an unrelated worktree must remain available")
	other.Unlock()
	release()
	require.True(t, lock.TryLock(), "admission must release every duplicate ancestor lock")
	lock.Unlock()
}

func TestWorktreeLifecycleRefusesChangedDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "directory")
	require.NoError(t, os.Mkdir(dir, 0o700))
	var lifecycle WorktreeLifecycle
	lifecycle.beforeAdmissionFn = func() { require.NoError(t, os.Remove(dir)) }
	release, err := lifecycle.admit(dir)
	require.ErrorContains(t, err, "directory changed during admission")
	assert.Nil(t, release)
	lock := lifecycle.directoryLock(pathutil.CanonicalizeAbsent(dir))
	require.True(t, lock.TryLock(), "refusal must release acquired locks")
	lock.Unlock()
}

func TestWorktreeLifecycleRefusesRetargetedSymlinkAndReleasesAllLocks(t *testing.T) {
	t.Parallel()
	stable := t.TempDir()
	original := t.TempDir()
	replacement := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(original, alias))
	var lifecycle WorktreeLifecycle
	lifecycle.beforeAdmissionFn = func() {
		require.NoError(t, os.Remove(alias))
		require.NoError(t, os.Symlink(replacement, alias))
	}
	release, err := lifecycle.admit(stable, alias)
	require.ErrorContains(t, err, "directory changed during admission")
	assert.Nil(t, release)
	resolved, err := filepath.EvalSymlinks(alias)
	require.NoError(t, err, "the changed directory must still exist")
	assert.True(t, pathutil.SamePath(resolved, pathutil.Canonicalize(replacement)))
	for _, dir := range []string{stable, original} {
		for ancestor := pathutil.Canonicalize(dir); ; ancestor = filepath.Dir(ancestor) {
			lock := lifecycle.directoryLock(ancestor)
			require.True(t, lock.TryLock(), "refusal must release every lock: %s", ancestor)
			lock.Unlock()
			if filepath.Dir(ancestor) == ancestor {
				break
			}
		}
	}
}

func TestWorktreeLifecycleAllowsMissingMetadataPaths(t *testing.T) {
	t.Parallel()
	var lifecycle WorktreeLifecycle
	release, err := lifecycle.admit("", filepath.Join(t.TempDir(), "absent"))
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
}
