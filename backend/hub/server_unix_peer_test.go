//go:build linux || darwin

package hub

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerUnixPeerNativeAdmissionFromSharedParent(t *testing.T) {
	local := locallistentest.UniqueListenURL(t, "lmx-shared-peer")
	path := strings.TrimPrefix(local, "unix:")
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o777))
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	server := startTestServer(t, &config.Config{Listen: []string{base, local}, SoloMode: true})
	requireAnswers(t, base)
	index := indexOfLocal(server.localListenURLs, local)
	require.GreaterOrEqual(t, index, 0)
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, listSettings(localIPCClientAt(t, server, index)), "the real same-account peer must reach the synthetic Solo administrator")
	setSoloPasswordDirect(t, server.store)
	require.NoError(t, listSettings(localIPCClientAt(t, server, index)), "native current-account verification must preserve local admission after password setup")
	err = listSettings(tcpClient(base))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
