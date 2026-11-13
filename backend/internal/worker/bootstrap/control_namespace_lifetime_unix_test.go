//go:build unix

package bootstrap

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWireOwnsDeepSocketNamespaceThroughShutdownAndRestart(t *testing.T) {
	data := filepath.Join(t.TempDir(), strings.Repeat("a", 120))
	otherData := filepath.Join(t.TempDir(), strings.Repeat("b", 120))
	require.NoError(t, os.MkdirAll(data, 0o700))
	require.NoError(t, os.MkdirAll(otherData, 0o700))
	first, _, _ := wireForTestWithKeypair(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { p.DataDir = data })
	sibling, _, _ := wireForTestWithKeypair(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { p.DataDir = otherData })
	factory := first.Service.ControlIPC.(*controlipc.Factory)
	other := sibling.Service.ControlIPC.(*controlipc.Factory)
	require.NotEqual(t, factory.SocketDir, other.SocketDir)
	info := service.AgentSpawnInfo{UserID: userid.MustNew("user-1"), WorkerID: "same-worker", TabID: "same-tab"}
	_, oldCleanup, err := factory.AgentSpawning(info)
	require.NoError(t, err)
	t.Cleanup(oldCleanup)
	_, otherCleanup, err := other.AgentSpawning(info)
	require.NoError(t, err)
	t.Cleanup(otherCleanup)
	refused, err := controlipc.PrepareSocketDirectory(data)
	require.Nil(t, refused)
	require.ErrorContains(t, err, "already in use")
	first.Shutdown()
	// These directly spawned listeners never entered Service's tab cleanup maps.
	// Wiring must therefore close the factory before it releases this namespace.
	afterShutdown, err := controlipc.PrepareSocketDirectory(data)
	require.NoError(t, err)
	assert.Equal(t, factory.SocketDir, afterShutdown.Path())
	require.NoError(t, afterShutdown.Close())
	orphanPath := filepath.Join(factory.SocketDir, "orphan.sock")
	orphan, err := net.ListenUnix("unix", &net.UnixAddr{Name: orphanPath, Net: "unix"})
	require.NoError(t, err)
	orphan.SetUnlinkOnClose(false)
	require.NoError(t, orphan.Close())
	ordinary := filepath.Join(factory.SocketDir, "ordinary")
	require.NoError(t, os.WriteFile(ordinary, []byte("keep"), 0o600))
	restarted, _, _ := wireForTestWithKeypair(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { p.DataDir = data; p.WorkerID = "new-worker" })
	replacement := restarted.Service.ControlIPC.(*controlipc.Factory)
	assert.Equal(t, factory.SocketDir, replacement.SocketDir, "registration identity cannot change directory ownership")
	_, err = os.Lstat(orphanPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, ordinary)
	_, replacementCleanup, err := replacement.AgentSpawning(info)
	require.NoError(t, err)
	t.Cleanup(replacementCleanup)
	oldCleanup()
	first.Shutdown()
	for _, directory := range []string{replacement.SocketDir, other.SocketDir} {
		path := strings.TrimPrefix(controlipc.SocketURL(directory, "same-worker", controlipc.SocketKindAgent, "same-tab"), "unix:")
		connection, err := net.DialTimeout("unix", path, time.Second)
		require.NoError(t, err)
		require.NoError(t, connection.Close())
	}
}
