package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orgEventsTestServer accepts org-events WebSockets and holds each open until the
// test ends, so a relay dialed against it stays live for ownership assertions.
func orgEventsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"orgevents-relay"}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	return server
}

// blockFirstOrgEventsDial makes the FIRST dial wait until the returned release
// channel is closed, and reports (via dialing) when it has entered. Later dials run
// straight through. This is the seam that makes the concurrent-open window -- which
// is otherwise a race no test could pin down -- deterministic.
func blockFirstOrgEventsDial(t *testing.T) (dialing chan struct{}, release chan struct{}) {
	t.Helper()
	dialing = make(chan struct{})
	release = make(chan struct{})
	original := dialOrgEvents
	t.Cleanup(func() { dialOrgEvents = original })
	var blocked atomic.Bool
	dialOrgEvents = func(ctx context.Context, proxy *HubProxy, orgID string, workspaceIDs []string) (*websocket.Conn, error) {
		if blocked.CompareAndSwap(false, true) {
			close(dialing)
			<-release
		}
		return original(ctx, proxy, orgID, workspaceIDs)
	}
	return dialing, release
}

// An open that lost the race must abandon its own dial, not force-restart over the
// relay a NEWER open already installed.
//
// OpenOrgEventsRelay deliberately force-restarts (the hub sends OrgMaterialized only
// at subscribe time, so adopting a live relay would leave a fresh page without its
// bootstrap), and RPCSession runs every request on its own goroutine with no
// ordering -- so without the fence both concurrent opens report success while the
// loser silently tears the winner's relay down. That teardown emits nothing (the
// relay context is cancelled before the read loop can emit an orgevents:close), so
// the page sits bootstrapped on a dead relay and awaitWorkspaceBootstrap burns its
// full 30s.
func TestApp_OpenOrgEventsRelay_ConcurrentOpenDoesNotTearDownTheSuccessor(t *testing.T) {
	server := orgEventsTestServer(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)

	dialing, release := blockFirstOrgEventsDial(t)

	// A opens first and blocks inside its dial.
	openA := make(chan error, 1)
	go func() { openA <- app.OpenOrgEventsRelay(context.Background(), 1, "org", nil) }()
	<-dialing

	// B -- a later attempt -- opens and installs while A is still dialing.
	require.NoError(t, app.OpenOrgEventsRelay(context.Background(), 2, "org", nil))
	app.lifecycleMu.RLock()
	relayB := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayB, "B's relay must be installed")
	require.Equal(t, uint64(2), relayB.owner, "the relay belongs to the attempt that opened it")

	close(release)
	require.Error(t, <-openA,
		"a superseded open must report that it lost, not claim a relay it abandoned")

	app.lifecycleMu.RLock()
	relayAfter := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayAfter, "the successor's relay must survive the loser's install")
	assert.Same(t, relayB, relayAfter, "last open dispatched wins, whatever order the sidecar runs them in")
	assert.NoError(t, relayAfter.ctx.Err(), "the surviving relay must still be live")
}

// A close for a relay a later open already replaced must be IGNORED.
//
// The frontend's tearDown/open pair dispatches the close first, but the sidecar may
// run it second; closing then kills the fresh relay, and because the teardown cancels
// the relay context before the read loop can emit, no orgevents:close ever reaches the
// page -- it stays bootstrapped on a dead relay, with the hub's one-shot
// OrgMaterialized never re-sent, until a reload.
func TestApp_CloseOrgEventsRelay_IgnoresStaleOwner(t *testing.T) {
	server := orgEventsTestServer(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)

	require.NoError(t, app.OpenOrgEventsRelay(context.Background(), 1, "org", nil))
	app.lifecycleMu.RLock()
	relayA := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayA)

	// The force-restart open hands the slot to a fresh relay owned by B.
	require.NoError(t, app.OpenOrgEventsRelay(context.Background(), 2, "org", nil))
	app.lifecycleMu.RLock()
	relayB := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayB)
	require.NotSame(t, relayA, relayB, "an org-events open force-restarts rather than adopting")

	// A's close arrives late. It must not touch B's relay.
	require.NoError(t, app.CloseOrgEventsRelay(1), "a stale close is satisfied, not an error")
	app.lifecycleMu.RLock()
	relayAfter := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayAfter, "a stale close must not tear down the successor's relay")
	assert.Same(t, relayB, relayAfter)
	assert.NoError(t, relayAfter.ctx.Err(), "the surviving relay must still be live")

	// B's own close still works.
	require.NoError(t, app.CloseOrgEventsRelay(2))
	app.lifecycleMu.RLock()
	relayFinal := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	assert.Nil(t, relayFinal, "the owner's close must tear the relay down")
}

// A stale open must not tear the newer relay down even before it dials: the pre-dial
// force-restart is the same hazard as the post-dial install, one lock acquisition
// earlier. Without the fence there the slot ends up EMPTY -- the stale open kills the
// successor and then abandons itself.
func TestApp_OpenOrgEventsRelay_StaleOpenLeavesTheNewerRelayInstalled(t *testing.T) {
	server := orgEventsTestServer(t)
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)

	require.NoError(t, app.OpenOrgEventsRelay(context.Background(), 9, "org", nil))
	app.lifecycleMu.RLock()
	relayBefore := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayBefore)

	require.Error(t, app.OpenOrgEventsRelay(context.Background(), 8, "org", nil),
		"an open that ran entirely after a newer one is stale")

	app.lifecycleMu.RLock()
	relayAfter := app.connection.orgEventsRelay
	app.lifecycleMu.RUnlock()
	require.NotNil(t, relayAfter, "the stale open must leave the newer relay installed")
	assert.Same(t, relayBefore, relayAfter)
	assert.NoError(t, relayAfter.ctx.Err(), "the surviving relay must still be live")
}

// dialOrgEvents must fail loudly when the proxy has no pinned WebSocket client.
// A nil wsClient would otherwise let channelwire.OpenOrgEventsWSWithHeader fall
// back to http.DefaultClient -- no cookie jar, no origin-redirect pin -- reopening
// the hub-side off-origin 3xx redirect-escape the pin closes. Both production
// constructors set wsClient, so this only fires for a future one that forgets it;
// it must break loudly, not silently dial unpinned.
func TestDialOrgEvents_FailsClosedWithoutPinnedWSClient(t *testing.T) {
	proxy := &HubProxy{baseURL: "http://hub.example"} // wsClient deliberately nil
	_, err := dialOrgEvents(context.Background(), proxy, "org", nil)
	require.Error(t, err, "a nil wsClient must fail the dial, not degrade to http.DefaultClient")
	assert.Contains(t, err.Error(), "pinned WebSocket client")
}

// The guard above only matters because production proxies always carry a pinned
// wsClient; assert that invariant so the two paths (guard + constructors) can't
// silently drift into both being false.
func TestProductionProxiesCarryAPinnedWSClient(t *testing.T) {
	remote := newHTTPProxy("https://hub.example")
	require.NotNil(t, remote.wsClient, "the remote hub proxy must set a WS-upgrade client")
	assert.NotNil(t, remote.wsClient.CheckRedirect, "and pin its redirects to the hub origin")

	local, err := newLocalProxy("unix:/tmp/leapmux-test.sock")
	require.NoError(t, err)
	require.NotNil(t, local.wsClient, "the local proxy must set a WS-upgrade client")
	assert.NotNil(t, local.wsClient.CheckRedirect, "and pin its redirects to the hub origin")
}
