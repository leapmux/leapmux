package lifecycle

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestCombinedLeaseTransfersEachComponentOnce(t *testing.T) {
	canonical := mustCanonical(t, t.TempDir())
	owner := NewDirectoryOwner(RoleSolo, canonical)
	root, err := AcquireDirectory(owner, RoleSolo, canonical)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	hub := root.TakeComponent(RoleHub)
	worker := root.TakeComponent(RoleWorker)
	require.NotNil(t, hub)
	require.NotNil(t, worker)
	t.Cleanup(func() { _ = hub.Close() })
	t.Cleanup(func() { _ = worker.Close() })
	assert.Equal(t, filepath.Join(canonical, "hub"), hub.Path())
	assert.Equal(t, filepath.Join(canonical, "worker"), worker.Path())
	assert.Equal(t, owner, hub.Owner())
	assert.Equal(t, owner, worker.Owner())
	assert.Nil(t, root.TakeComponent(RoleHub))
	assert.Nil(t, root.TakeComponent(RoleWorker))
	require.NoError(t, root.Close())
	assert.False(t, hub.Closed(), "transferred components belong to their runtime")
	assert.False(t, worker.Closed())
	assert.Nil(t, root.TakeComponent(RoleHub))
}

func TestCombinedLeaseClosesUntransferredComponents(t *testing.T) {
	canonical := mustCanonical(t, t.TempDir())
	owner := NewDirectoryOwner(RoleDev, canonical)
	root, err := AcquireDirectory(owner, RoleDev, canonical)
	require.NoError(t, err)
	hub, worker := root.components[RoleHub], root.components[RoleWorker]
	require.NoError(t, root.Close())
	require.NoError(t, root.Close())
	assert.True(t, hub.Closed())
	assert.True(t, worker.Closed())
	replacement, err := AcquireDirectory(owner, RoleDev, canonical)
	require.NoError(t, err, "root cleanup must release every retained component")
	require.NoError(t, replacement.Close())
}

func TestCombinedLeaseTransferAndCloseAreConcurrent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	canonical := mustCanonical(t, t.TempDir())
	root, err := AcquireDirectory(NewDirectoryOwner(RoleSolo, canonical), RoleSolo, canonical)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	original := root.components[RoleWorker]
	release := make(chan struct{})
	taken := make(chan *DirectoryLease, 1)
	closed := make(chan error, 1)
	go func() {
		<-release
		taken <- root.TakeComponent(RoleWorker)
	}()
	go func() {
		<-release
		closed <- root.Close()
	}()
	close(release)
	var transferred *DirectoryLease
	select {
	case transferred = <-taken:
	case <-ctx.Done():
		t.Fatal("component transfer did not finish")
	}
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("root cleanup did not finish")
	}
	if transferred == nil {
		assert.True(t, original.Closed())
	} else {
		assert.False(t, transferred.Closed(), "root cleanup cannot close a transferred lease")
		require.NoError(t, transferred.Close())
	}
}

func TestCombinedAcquisitionReleasesPartialComponents(t *testing.T) {
	canonical := mustCanonical(t, t.TempDir())
	workerPath := filepath.Join(canonical, "worker")
	worker, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, workerPath), RoleWorker, workerPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	owner := NewDirectoryOwner(RoleSolo, canonical)
	_, err = AcquireDirectory(owner, RoleSolo, canonical)
	require.Error(t, err)
	hub, err := AcquireDirectory(owner, RoleHub, filepath.Join(canonical, "hub"))
	require.NoError(t, err, "a refused combined acquisition must release its earlier Hub lease")
	require.NoError(t, hub.Close())
	held, err := ProbeLockAt(PathsOf(canonical).DataLock)
	require.NoError(t, err)
	assert.False(t, held, "the refused acquisition must release the root lease too")
}

func TestLeaseCloseRetainsNativeFailuresAndReleasesComponents(t *testing.T) {
	for _, targetRole := range []Role{RoleSolo, RoleHub, RoleWorker} {
		t.Run(targetRole.String(), func(t *testing.T) {
			rootPath := mustCanonical(t, t.TempDir())
			owner := NewDirectoryOwner(RoleSolo, rootPath)
			root, err := AcquireDirectory(owner, RoleSolo, rootPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = root.Close() })
			target := root
			if targetRole != RoleSolo {
				target = root.components[targetRole]
			}
			require.NoError(t, target.file.Close())
			start := make(chan struct{})
			results := make(chan error, 32)
			var callers sync.WaitGroup
			for range 32 {
				callers.Go(func() { <-start; results <- root.Close() })
			}
			close(start)
			callers.Wait()
			close(results)
			var retained error
			for result := range results {
				require.ErrorIs(t, result, os.ErrClosed)
				if retained == nil {
					retained = result
				} else {
					assert.Same(t, retained, result)
				}
			}
			assert.Same(t, retained, root.Close())
			replacement, err := AcquireDirectory(owner, RoleSolo, rootPath)
			require.NoError(t, err, "all retained component locks must release despite a native close failure")
			require.NoError(t, replacement.Close())
		})
	}
}
