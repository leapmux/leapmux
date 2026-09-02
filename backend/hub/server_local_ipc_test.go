package hub

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/locallisten"
)

// localIPCClient dials the hub's local IPC listener -- the unix socket on Unix,
// the named pipe on Windows -- the way the desktop shell's sidecar does.
func localIPCClient(t *testing.T, srv *Server) leapmuxv1connect.AdminSettingsServiceClient {
	t.Helper()
	dial, err := locallisten.Dialer(srv.listenURL)
	require.NoError(t, err)
	httpClient := &http.Client{
		Transport: &http.Transport{DialContext: locallisten.HTTPDialContext(dial)},
		Timeout:   10 * time.Second,
	}
	return leapmuxv1connect.NewAdminSettingsServiceClient(httpClient, locallisten.LocalConnectURL)
}

// tcpClient dials one of the hub's TCP addresses, with keep-alives off so no
// pooled connection outlives the listener that accepted it.
func tcpClient(addr string) leapmuxv1connect.AdminSettingsServiceClient {
	httpClient := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   10 * time.Second,
	}
	return leapmuxv1connect.NewAdminSettingsServiceClient(httpClient, "http://"+addr)
}

// listSettings runs one admin call carrying nothing at all: no cookie, no
// bearer. Only a caller the solo rung admits can succeed.
func listSettings(c leapmuxv1connect.AdminSettingsServiceClient) error {
	_, err := c.ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
	return err
}

// The local IPC socket is the ONE credential-free transport, and the desktop
// app rests entirely on it: it reaches its own hub over that socket and has no
// way to present a password, so a hub that asked it for one would be unusable
// with no remedy inside the product.
//
// The rule is decided by auth.SoloGate and the mark is placed by the
// http.Server's BaseContext, which compares the accepting listener against
// localLn. Every other test of this arm builds the marked context BY HAND, so
// all of them pass whatever that comparison does. This one drives real
// connections through the real server, which is the only way the wiring itself
// is under test.
//
// Both arms are asserted from ONE hub, after ONE password write, because the
// two answers have to diverge on the same process: a rule that refused both or
// admitted both would satisfy either arm alone.
func TestServer_TheLocalSocketStaysCredentialFreeWhenTCPStopsBeing(t *testing.T) {
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	// The bootstrap state: neither transport asks for anything.
	require.NoError(t, listSettings(localIPCClient(t, srv)),
		"the local socket must admit the desktop app before a password exists")
	require.NoError(t, listSettings(tcpClient(base)),
		"TCP must stay credential-free while the account has no password, or the first password could never be set from a browser")

	setSoloPasswordDirect(t, srv.store)

	// TCP arms itself with no restart: the gate re-reads the account while its
	// latch is false.
	err := listSettings(tcpClient(base))
	require.Error(t, err, "TCP must ask for a sign-in once the account holds a password")
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// And the local socket does not, which is the whole point. A regression
	// here locks the desktop app out of its own hub.
	require.NoError(t, listSettings(localIPCClient(t, srv)),
		"the local socket is the one exception, and the desktop app cannot present a password")
}

// setSoloPasswordDirect stores a usable hash on the solo account, bypassing
// ChangePassword: this test is about the TRANSPORT rule, and the RPC would drag
// in the session handover that has tests of its own.
func setSoloPasswordDirect(t *testing.T, st store.Store) {
	t.Helper()
	user, err := st.Users().GetByUsername(context.Background(), usernames.Solo)
	require.NoError(t, err)
	hash, err := password.Hash("correct-horse-battery-staple")
	require.NoError(t, err)
	require.NoError(t, st.Users().UpdatePassword(context.Background(), store.UpdateUserPasswordParams{
		PasswordHash: hash, ID: user.ID,
	}))
}
