//go:build unix && !linux && !darwin

package locallisten

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedUnixPeerVerificationRefusesBeforeBindOrStaleRemoval(t *testing.T) {
	root, err := os.MkdirTemp("", "lp")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	existing := filepath.Join(root, "old.sock")
	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: existing, Net: "unix"})
	require.NoError(t, err)
	raw.SetUnlinkOnClose(false)
	require.NoError(t, raw.Close())
	before, err := os.Lstat(existing)
	require.NoError(t, err)
	permissionCalled := false
	listener, err := listenUnixWithPermissions(existing, func(string, os.FileMode) error { permissionCalled = true; return nil })
	if listener != nil {
		t.Cleanup(func() { _ = listener.Close() })
	}
	require.Nil(t, listener)
	require.ErrorIs(t, err, ErrUnsupportedScheme)
	assert.False(t, permissionCalled)
	after, err := os.Lstat(existing)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "an unsupported platform cannot remove or replace an existing socket")
	missing := filepath.Join(root, "new.sock")
	listener, err = Listen("unix:" + missing)
	if listener != nil {
		t.Cleanup(func() { _ = listener.Close() })
	}
	require.Nil(t, listener)
	require.ErrorIs(t, err, ErrUnsupportedScheme)
	_, err = os.Lstat(missing)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
