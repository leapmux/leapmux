package service

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeDirectoryMatchesFiltersAndDeduplicates(t *testing.T) {
	t.Parallel()
	_, wt, _ := existingCheckoutForSafetyTest(t)
	top := pathutil.Canonicalize(wt)
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(wt, alias))
	unrelated := t.TempDir()
	dirs := []string{alias, unrelated, filepath.Join(unrelated, "missing"), ""}
	for range 100 {
		dirs = append(dirs, wt)
	}
	var calls atomic.Int32
	probe := newBranchInfoProbe(branchProbeConcurrency)
	probe.query = func(ctx context.Context, dir string) (*gitPathInfo, error) {
		calls.Add(1)
		return queryGitPathInfo(ctx, dir)
	}
	matches := matchWorktreeDirectories(context.Background(), top, dirs, probe)
	assert.True(t, matches[wt].matches)
	assert.True(t, matches[alias].matches)
	assert.False(t, matches[unrelated].matches)
	assert.NoError(t, matches[filepath.Join(unrelated, "missing")].err)
	assert.Equal(t, int32(1), calls.Load(), "duplicate aliases and unrelated paths must not start extra Git processes")
}

func TestWorktreeDirectoryMatchesRetainsUnresolvedTargetReferences(t *testing.T) {
	t.Parallel()
	_, wt, _ := existingCheckoutForSafetyTest(t)
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(wt, alias))
	broken := filepath.Join(t.TempDir(), "broken")
	require.NoError(t, os.Symlink(filepath.Join(wt, "missing"), broken))
	dirs := []string{filepath.Join(wt, "missing"), filepath.Join(alias, "missing"), broken}
	matches := matchWorktreeDirectories(context.Background(), pathutil.Canonicalize(wt), dirs, newBranchInfoProbe(branchProbeConcurrency))
	for _, dir := range dirs {
		assert.Error(t, matches[dir].err, "an unresolved target reference must prevent deletion: %s", dir)
	}
}
