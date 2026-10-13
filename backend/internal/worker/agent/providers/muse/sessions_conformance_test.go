//
// This suite needs a Windows bring-up that only a local Windows run can state:
// the session harness starts the CLI, and the Windows runner cannot start it.
// The suite keeps its full coverage on macOS and Linux.

package muse

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
//
// The peer re-executes the test binary, because a `#!/bin/sh` script cannot
// run on Windows and the suite keeps its coverage on every platform.
const museStoredSessionPeerEnv = "LEAPMUX_MUSE_STORED_SESSION_PEER"

func TestMuseStoredSessionPeer(t *testing.T) {
	if os.Getenv(museStoredSessionPeerEnv) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal([]byte(line), &request) != nil {
			continue
		}
		switch {
		case request.Method == "initialize":
			fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"muse","version":"peer"},"museHome":"","grantedCapabilities":[],"experimentalApi":true,"sessionDurability":"","schema":{"version":1,"fingerprint":"peer-schema"}}}`)
		case strings.Contains(request.Method, "session/list"):
			fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"sessions":[{"sessionId":"` + museStoredSessionPeerHandle + `","title":"Peer session","createdAt":"2026-10-09T00:00:00Z","updatedAt":"2026-10-09T00:01:00Z"}],"nextCursor":null}}`)
		}
	}
	os.Exit(0)
}

// newStoredSessionPeerCLI states the controlled peer by its absolute path, so
// no PATH entry -- and no shell profile -- can put the developer's real Muse
// CLI in its place.
func newStoredSessionPeerCLI(t *testing.T) launch.Locator {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{
			Program:    executable,
			PrefixArgs: []string{"-test.run=^TestMuseStoredSessionPeer$", "--"},
			Env:        []string{museStoredSessionPeerEnv + "=1"},
		}, launch.Found
	})
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
