package solo

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func TestStartPreparedPreservesNoTCP(t *testing.T) {
	locallistentest.SandboxHome(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, occupied.Close()) })
	local := uniqueTestListenURL(t)
	prepared, err := Prepare(Config{
		Args: []string{
			"--data-dir", t.TempDir(),
			"--listen", occupied.Addr().String(),
			"--listen", local,
		},
		NoTCP:      true,
		SkipBanner: true,
	})
	require.NoError(t, err)
	root, err := lifecycle.AcquireDirectory(
		lifecycle.NewDirectoryOwner(prepared.Role(), prepared.DataDirectory()),
		prepared.Role(), prepared.DataDirectory())
	require.NoError(t, err)
	inst, err := StartPrepared(context.Background(), prepared, root)
	require.NoError(t, err, "NoTCP must exclude the occupied TCP address before binding")
	t.Cleanup(func() { require.NoError(t, inst.Stop()) })
	assert.Equal(t, local, inst.LocalListenURL())
}

func TestStartPreparedPreservesSkipBanner(t *testing.T) {
	locallistentest.SandboxHome(t)
	output := testutil.CaptureStderr(t, func() {
		inst, err := Start(context.Background(), Config{
			Args:       []string{"--listen", uniqueTestListenURL(t), "--data-dir", t.TempDir()},
			SkipBanner: true,
		})
		require.NoError(t, err)
		require.NoError(t, inst.Stop())
	})
	assert.NotContains(t, output, "Copyright", "the prepared launch must retain banner suppression")
}

// Prepare derives the same defaults and flags Start resolves, with no
// filesystem writes: the canonical root and the containing role of a
// fresh launch.
func TestPrepareResolvesWithoutWrites(t *testing.T) {
	locallistentest.SandboxHome(t)

	canonicalRoot := t.TempDir()
	args := []string{"--listen=" + uniqueTestListenURL(t), "--data-dir=" + canonicalRoot}

	before := snapshotTree(t, canonicalRoot)

	prepared, err := Prepare(Config{Args: args, SkipBanner: true})
	require.NoError(t, err)
	assert.Equal(t, lifecycle.RoleSolo, prepared.Role())
	assert.Equal(t, mustCanonicalTest(t, canonicalRoot), prepared.DataDirectory())

	assert.Equal(t, snapshotTree(t, canonicalRoot), before,
		"preparation writes nothing into the data root")
}

// TestStartPreparedRejectsInvalidRootLease pins the call boundary: a
// nil preparation or any invalid root lease is rejected BEFORE any
// component acquisition, state access, registration, or listener
// creation runs, the prepared root stays byte-for-byte unchanged, and
// any supplied lease is released exactly once.
func TestStartPreparedRejectsInvalidRootLease(t *testing.T) {
	locallistentest.SandboxHome(t)

	freshPrepared := func(t *testing.T) *PreparedConfig {
		t.Helper()
		root := t.TempDir()
		prepared, err := Prepare(Config{
			Args:       []string{"--listen=" + uniqueTestListenURL(t), "--data-dir=" + root},
			SkipBanner: true,
			NoTCP:      true,
		})
		require.NoError(t, err)
		return prepared
	}
	freshLease := func(t *testing.T, prepared *PreparedConfig) *lifecycle.DirectoryLease {
		t.Helper()
		lease, err := lifecycle.AcquireDirectory(
			lifecycle.NewDirectoryOwner(prepared.role, prepared.dataDir), prepared.role, prepared.dataDir)
		require.NoError(t, err)
		return lease
	}

	for _, tc := range []struct {
		name  string
		build func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease)
	}{
		{"nil preparation", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			lease := freshLease(t, freshPrepared(t))
			return nil, lease
		}},
		{"nil lease", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			return freshPrepared(t), nil
		}},
		{"closed lease", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			prepared := freshPrepared(t)
			lease := freshLease(t, prepared)
			require.NoError(t, lease.Close())
			return prepared, lease
		}},
		{"another canonical root", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			prepared := freshPrepared(t)
			other := freshPrepared(t)
			return prepared, freshLease(t, other)
		}},
		{"wrong component mode", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			prepared := freshPrepared(t)
			hubLease, err := lifecycle.AcquireDirectory(
				lifecycle.NewDirectoryOwner(prepared.role, prepared.dataDir), lifecycle.RoleHub, prepared.dataDir)
			require.NoError(t, err)
			return prepared, hubLease
		}},
		{"another containing owner", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			prepared := freshPrepared(t)
			lease, err := lifecycle.AcquireDirectory(lifecycle.DirectoryOwner{
				AccountID: lifecycle.CurrentAccountID(),
				Mode:      lifecycle.RoleDev,
				Root:      prepared.dataDir,
			}, prepared.role, prepared.dataDir)
			require.NoError(t, err)
			return prepared, lease
		}},
		{"another containing root", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			prepared := freshPrepared(t)
			lease, err := lifecycle.AcquireDirectory(lifecycle.DirectoryOwner{AccountID: lifecycle.CurrentAccountID(), Mode: prepared.role, Root: mustCanonicalTest(t, t.TempDir())}, prepared.role, prepared.dataDir)
			require.NoError(t, err)
			return prepared, lease
		}},
		{"another account", func(t *testing.T) (*PreparedConfig, *lifecycle.DirectoryLease) {
			prepared := freshPrepared(t)
			lease, err := lifecycle.AcquireDirectory(lifecycle.DirectoryOwner{AccountID: "another-account", Mode: prepared.role, Root: prepared.dataDir}, prepared.role, prepared.dataDir)
			require.NoError(t, err)
			return prepared, lease
		}},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			prepared, lease := tc.build(t)
			before := ""
			if prepared != nil {
				for _, path := range []string{"hub/hub.db", "worker/worker.db", "worker/state.json"} {
					destination := filepath.Join(prepared.dataDir, filepath.FromSlash(path))
					require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o700))
					require.NoError(t, os.WriteFile(destination, []byte("preserve "+path), 0o600))
				}
				before = snapshotTree(t, prepared.dataDir)
			}

			inst, err := StartPrepared(context.Background(), prepared, lease)
			require.Error(t, err)
			assert.Nil(t, inst)

			if prepared != nil {
				assert.Equal(t, before, snapshotTree(t, prepared.dataDir),
					"the prepared root is unchanged by the rejection")
			}
			// A non-nil supplied lease was released exactly once: it is
			// closed now, and a second release stays a no-op.
			if lease != nil {
				assert.True(t, lease.Closed(), "the rejected lease is released")
				assert.NoError(t, lease.Close(), "the release is idempotent")
			}
		})
	}
}

// snapshotTree lists a directory tree's relative paths, the byte-level
// shape a rejection must leave untouched.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return "<absent>"
	}
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&out, "%s %s", relative, entry.Type())
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, " %x", data)
		}
		out.WriteByte('\n')
		return nil
	})
	require.NoError(t, err)
	return out.String()
}

// uniqueTestListenURL names one local IPC endpoint for this test.
func uniqueTestListenURL(t *testing.T) string {
	return locallistentest.UniqueListenURL(t, "leapmux-solo-prepare")
}

func mustCanonicalTest(t *testing.T, dir string) string {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(dir)
	require.NoError(t, err)
	return canonical
}
