//go:build unix

package bootstrap

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

func TestNewControlIPCFactory_SweepsLeftoverSockets(t *testing.T) {
	dataDir, err := os.MkdirTemp("/tmp", "leapmux-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dataDir)) })
	dir := controlipc.SocketDir(dataDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "stale.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())
	_, _, _ = wireForTestWithKeypair(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { p.DataDir = dataDir })
	_, err = os.Lstat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
