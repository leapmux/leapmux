package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moveInstance acquires and releases an instance at a first path, leaves the
// path-bound records that a configured service and a finished attempt leave
// behind, then moves the whole directory to a second path.
func moveInstance(t *testing.T, mode Role) (from, to string) {
	t.Helper()
	parent := mustCanonical(t, t.TempDir())
	from = filepath.Join(parent, "before")
	lease, err := AcquireDirectory(NewDirectoryOwner(mode, from), mode, from)
	require.NoError(t, err)
	require.NoError(t, lease.Close())
	paths := pathsFor(from)
	require.NoError(t, WriteServiceSpec(paths.ServiceSpec, ServiceSpec{Mode: mode, DataDirectory: from, InstanceID: "old"}))
	require.NoError(t, WriteRuntimeRecord(paths.RuntimeRecord, RuntimeRecord{InstanceID: "old", Mode: mode, DataDir: from, StartedAt: time.Now()}))
	to = filepath.Join(parent, "after")
	require.NoError(t, os.Rename(from, to))
	return from, to
}

func TestAcquireDirectoryAdoptsADirectoryThatMovedWithItsInstance(t *testing.T) {
	for _, mode := range []Role{RoleHub, RoleWorker} {
		t.Run(mode.String(), func(t *testing.T) {
			_, to := moveInstance(t, mode)
			paths := pathsFor(to)

			lease, err := AcquireDirectory(NewDirectoryOwner(mode, to), mode, to)
			require.NoError(t, err)
			require.NoError(t, lease.Close())

			record, err := readOwnershipRecord(paths.OwnershipRecord)
			require.NoError(t, err)
			assert.Equal(t, to, record.DataDirectory)
			assert.Equal(t, to, record.OwnerDataDir)
			assert.Equal(t, mode, record.Mode)
			assert.NoFileExists(t, paths.ServiceSpec, "the service settings of the old path must go")
			assert.NoFileExists(t, paths.RuntimeRecord, "the attempt record of the old path must go")

			again, err := AcquireDirectory(NewDirectoryOwner(mode, to), mode, to)
			require.NoError(t, err, "an adopted directory is an ordinary directory")
			require.NoError(t, again.Close())
		})
	}
}

func TestAcquireDirectoryAdoptsAMovedCombinedRootAndItsComponents(t *testing.T) {
	_, to := moveInstance(t, RoleSolo)

	root, err := AcquireDirectory(NewDirectoryOwner(RoleSolo, to), RoleSolo, to)
	require.NoError(t, err)
	require.NoError(t, root.Close())

	for _, component := range []Role{RoleHub, RoleWorker} {
		dir := filepath.Join(to, component.String())
		record, err := readOwnershipRecord(pathsFor(dir).OwnershipRecord)
		require.NoError(t, err)
		assert.Equal(t, dir, record.DataDirectory)
		assert.Equal(t, to, record.OwnerDataDir)
		assert.Equal(t, RoleSolo, record.OwnerMode)
	}
}

// Only the same instance at a new path is a move. Each of these keeps the
// ownership conflict.
func TestAcquireDirectoryRefusesWhatIsNotAMove(t *testing.T) {
	t.Run("another mode at the moved path", func(t *testing.T) {
		_, to := moveInstance(t, RoleHub)
		_, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, to), RoleWorker, to)
		var conflict *OwnershipConflict
		require.ErrorAs(t, err, &conflict)
		assert.FileExists(t, pathsFor(to).ServiceSpec, "a refused acquisition must change nothing")
	})
	t.Run("another account", func(t *testing.T) {
		_, to := moveInstance(t, RoleHub)
		owner := NewDirectoryOwner(RoleHub, to)
		owner.AccountID = "another-account"
		_, err := AcquireDirectory(owner, RoleHub, to)
		var conflict *OwnershipConflict
		require.ErrorAs(t, err, &conflict)
	})
	t.Run("a second combined root that claims an unmoved component", func(t *testing.T) {
		component := mustCanonical(t, t.TempDir())
		first, err := AcquireDirectory(NewDirectoryOwner(RoleSolo, mustCanonical(t, t.TempDir())), RoleHub, component)
		require.NoError(t, err)
		require.NoError(t, first.Close())
		_, err = AcquireDirectory(NewDirectoryOwner(RoleSolo, mustCanonical(t, t.TempDir())), RoleHub, component)
		var conflict *OwnershipConflict
		require.ErrorAs(t, err, &conflict, "an unchanged path with another owner is a second instance")
	})
	t.Run("a component moved away from its root", func(t *testing.T) {
		parent := mustCanonical(t, t.TempDir())
		root := filepath.Join(parent, "solo")
		lease, err := AcquireDirectory(NewDirectoryOwner(RoleSolo, root), RoleSolo, root)
		require.NoError(t, err)
		require.NoError(t, lease.Close())
		alone := filepath.Join(parent, "hub-alone")
		require.NoError(t, os.Rename(filepath.Join(root, "hub"), alone))
		otherRoot := filepath.Join(parent, "other-solo")
		_, err = AcquireDirectory(NewDirectoryOwner(RoleSolo, otherRoot), RoleHub, alone)
		var conflict *OwnershipConflict
		require.ErrorAs(t, err, &conflict, "the component left its root, so the instance did not move as a whole")
	})
}

// The commands that read a directory without its data lock accept a moved
// directory and ignore the records of the old path, so status reports a
// stopped instance with no service until a command adopts it.
func TestUnlockedReadersAcceptAMovedDirectory(t *testing.T) {
	from, to := moveInstance(t, RoleHub)
	paths := pathsFor(to)

	require.NoError(t, ValidateDirectorySelection(RoleHub, to))
	var conflict *OwnershipConflict
	require.ErrorAs(t, ValidateDirectorySelection(RoleWorker, to), &conflict)

	_, err := ReadServiceSpecOf(paths)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Contains(t, err.Error(), from)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := Observe(ctx, to)
	require.NoError(t, err)
	assert.Equal(t, ProcessStopped, status.Process.State)
	assert.False(t, status.Configured)
	assert.Nil(t, status.Attempt.ID, "the attempt of the old path is not this instance's")

	spec, err := ReadServiceSpecOf(pathsFor(mustCanonical(t, t.TempDir())))
	require.True(t, errors.Is(err, os.ErrNotExist), "a missing specification still reads as absent")
	assert.Empty(t, spec.DataDirectory)
}

// A moved combined root passes the unlocked selection check for its own mode
// before any command adopts it, and its components stay unadopted until then.
func TestUnlockedSelectionAcceptsAMovedCombinedRoot(t *testing.T) {
	from, to := moveInstance(t, RoleSolo)

	require.NoError(t, ValidateDirectorySelection(RoleSolo, to))
	var conflict *OwnershipConflict
	require.ErrorAs(t, ValidateDirectorySelection(RoleDev, to), &conflict, "another combined mode is no move")

	component, err := readOwnershipRecord(pathsFor(filepath.Join(to, RoleHub.String())).OwnershipRecord)
	require.NoError(t, err)
	assert.Equal(t, from, component.OwnerDataDir, "a check without the data lock must not adopt anything")
}
