//go:build unix

package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Existing lifecycle resources that another account owns, or that are
// wider than the account, refuse startup BEFORE anything is modified.
// A test process cannot become another uid, so the foreign-account case
// plants the ownership record of another account and the permission
// cases widen the real files.
func TestPrivateResourcesRejectForeignOwner(t *testing.T) {
	t.Run("a wider lifecycle directory is refused before modification", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "wide")
		paths := pathsFor(mustCanonical(t, root))
		require.NoError(t, os.MkdirAll(paths.Dir, 0o755))
		t.Cleanup(func() { _ = os.Chmod(paths.Dir, 0o700) })

		_, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, mustCanonical(t, root)), RoleWorker, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wider than this account")
		assert.NoFileExists(t, paths.OwnershipRecord, "the refusal modified nothing")
	})

	t.Run("a wider token is refused", func(t *testing.T) {
		runtimeEnv(t)
		root := filepath.Join(t.TempDir(), "wide-token")
		lease := acquireForTest(t, root)
		paths := pathsFor(mustCanonical(t, root))
		require.NoError(t, WriteControlToken(paths.ControlToken, "token-material"))
		require.NoError(t, os.Chmod(paths.ControlToken, 0o644))
		t.Cleanup(func() { _ = os.Chmod(paths.ControlToken, 0o600) })

		err := verifyPrivateFile(paths.ControlToken)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wider than this account")
		require.NoError(t, lease.Close())
	})

	t.Run("another account's ownership record is a conflict", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "foreign")
		paths := pathsFor(mustCanonical(t, root))
		lease := acquireForTest(t, root)
		foreign := ownershipRecord{
			AccountID:     "1",
			Mode:          RoleWorker,
			DataDirectory: mustCanonical(t, root),
			OwnerMode:     RoleWorker,
			OwnerDataDir:  mustCanonical(t, root),
		}
		require.NoError(t, writeOwnershipRecord(paths.OwnershipRecord, foreign))
		// The original stopped: its lease released, its marker stayed.
		require.NoError(t, lease.Close())

		conflicting, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, mustCanonical(t, root)), RoleWorker, root)
		require.Error(t, err)
		assert.Nil(t, conflicting)
		var conflict *OwnershipConflict
		require.ErrorAs(t, err, &conflict)

		// The marker is intact: a foreign record is not permission to
		// overwrite it.
		stored, readErr := readOwnershipRecord(paths.OwnershipRecord)
		require.NoError(t, readErr)
		assert.Equal(t, foreign, stored)
	})
}

// acquireForTest takes a lease the test releases at cleanup.
func acquireForTest(t *testing.T, root string) *DirectoryLease {
	t.Helper()
	lease, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, mustCanonical(t, root)), RoleWorker, root)
	require.NoError(t, err)
	return lease
}
