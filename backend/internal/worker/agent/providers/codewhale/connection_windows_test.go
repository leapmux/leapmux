package codewhale

import (
	"net"
	"os"
	"testing"

	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/procutil"
)

func TestWindowsRuntimeListenerSnapshotIdentifiesANativeSocket(t *testing.T) {
	t.Parallel()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	identity, found := procutil.IdentifyProcess(os.Getpid())
	require.True(t, found)
	current, found := runtimeListenerFromSnapshot(t.Context(), []procutil.ProcessIdentity{identity}, listener.Addr().(*net.TCPAddr).Port, gopsnet.ConnectionsWithContext)
	require.True(t, found)
	assert.Equal(t, identity, current)
}
