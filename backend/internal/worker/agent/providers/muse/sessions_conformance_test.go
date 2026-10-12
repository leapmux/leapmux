package muse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// museStoredSessionPeerHandle is the one session the controlled peer serves.
const museStoredSessionPeerHandle = "ses_muse_peer_stored"

// The controlled `muse` speaks the minimal JSON-RPC the stored-session reader
// uses. Each reply hardcodes its request id, because the reader's ids are a
// per-connection counter and this peer answers exactly two requests: the
// initialize handshake (id 1) and one session/list page (id 2). Everything on
// a line the peer does not recognize -- the ready delimiter, the initialized
// notification -- needs no reply and gets none.
const museStoredSessionPeerScript = `#!/bin/sh
while IFS= read -r line; do
  case $line in
    *initialize*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"muse","version":"peer"},"museHome":"","grantedCapabilities":[],"experimentalApi":true,"sessionDurability":"","schema":{"version":1,"fingerprint":"peer-schema"}}}'
      ;;
    *session/list*)
      printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessions":[{"sessionId":"ses_muse_peer_stored","title":"Peer session","createdAt":"2026-10-09T00:00:00Z","updatedAt":"2026-10-09T00:01:00Z"}],"nextCursor":null}}'
      ;;
  esac
done
`

// newStoredSessionPeerCLI states the controlled peer by its absolute path, so
// no PATH entry -- and no shell profile -- can put the developer's real Muse
// CLI in its place.
func newStoredSessionPeerCLI(t *testing.T) launch.Locator {
	t.Helper()
	path := filepath.Join(t.TempDir(), "muse")
	require.NoError(t, os.WriteFile(path, []byte(museStoredSessionPeerScript), 0o755))
	return launch.Binaries(path)
}

func TestMuseReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	locator := newStoredSessionPeerCLI(t)
	plugin := museProvider{locator: &locator}
	agenttest.RequireReadsSessionStore(t, plugin, func(t *testing.T, _, dir string) string {
		require.NoError(t, os.MkdirAll(dir, 0o700))
		return museStoredSessionPeerHandle
	})
}

var _ agent.Provider = museProvider{}
