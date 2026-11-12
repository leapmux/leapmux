//go:build unix

package controlipc_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/service"
)

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "leapmux-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

func TestFactory_SocketIsNotInTheSharedTempDir(t *testing.T) {
	tmp := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", tmp)
	f := &controlipc.Factory{WorkerID: "worker-A", SocketDir: shortSocketDir(t)}
	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	socket := envValue(t, envs, "LEAPMUX_CONTROL_SOCK")
	require.NotEmpty(t, socket)
	assert.False(t, strings.HasPrefix(socket, "unix:"+tmp+"/"), socket)
	assert.True(t, strings.HasPrefix(socket, "unix:"+f.SocketDir+"/"), socket)
}

func TestFactory_SocketDirectoryValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T) string
		message string
	}{
		{"empty", func(*testing.T) string { return "" }, "must be absolute"},
		{"relative", func(*testing.T) string { return "run/control" }, "must be absolute"},
		{"wide permissions", func(t *testing.T) string {
			dir := shortSocketDir(t)
			require.NoError(t, os.Chmod(dir, 0o755))
			return dir
		}, "wider than this account"},
		{"another owner", func(t *testing.T) string {
			if os.Geteuid() == 0 {
				t.Skip("root owns the test directory")
			}
			return "/"
		}, "belongs to another account"},
		{"symlink", func(t *testing.T) string {
			dir := shortSocketDir(t)
			link := filepath.Join(dir, "link")
			require.NoError(t, os.Symlink(shortSocketDir(t), link))
			return link
		}, "not a private directory"},
		{"file parent", func(t *testing.T) string {
			parent := filepath.Join(shortSocketDir(t), "run")
			require.NoError(t, os.WriteFile(parent, nil, 0o600))
			return filepath.Join(parent, "control")
		}, "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.prepare(t)
			factory := &controlipc.Factory{SocketDir: dir}
			env, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"})
			require.ErrorContains(t, err, tc.message)
			assert.Nil(t, cleanup)
			assert.Empty(t, env)
			_, statErr := os.Lstat(filepath.Join(dir, "a-agent-1.sock"))
			assert.Error(t, statErr)
		})
	}
}

func TestFactory_CreatesAndRecreatesThePrivateSocketDirectory(t *testing.T) {
	dir := filepath.Join(shortSocketDir(t), "control")
	factory := &controlipc.Factory{SocketDir: dir}
	for range 2 {
		env, cleanup, err := factory.AgentSpawning(service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"})
		require.NoError(t, err)
		require.NotEmpty(t, env)
		info, err := os.Lstat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		cleanup()
		require.NoError(t, os.RemoveAll(dir))
	}
}

func TestSweepSocketDir_RemovesOnlyLeftoverSockets(t *testing.T) {
	dir := shortSocketDir(t)
	socket := filepath.Join(dir, "stale.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())
	for _, name := range []string{"regular.sock", "other"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o600))
	}
	require.NoError(t, controlipc.SweepSocketDir(dir))
	_, err = os.Lstat(socket)
	assert.ErrorIs(t, err, os.ErrNotExist)
	for _, name := range []string{"regular.sock", "other"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, "keep", string(data))
	}
}
