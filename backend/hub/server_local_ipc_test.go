package hub

import (
	"context"
	"encoding/json"
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
	"github.com/leapmux/leapmux/internal/hub/requestsource"
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

// A passwordless solo TCP connection can claim the account by setting its
// first password. It must not receive the synthetic administrator before that
// write. Otherwise, any caller that reaches the port can call every protected
// RPC without first taking responsibility for the account.
func TestServer_PasswordlessSoloTCPRejectsAdministratorRPC(t *testing.T) {
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	err := listSettings(tcpClient(base))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestServer_TCPInitialSoloPasswordCreatesVerifiedSession(t *testing.T) {
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)
	require.NoError(t, srv.settings.Update(context.Background(), requestsource.KeyTrustedProxyRanges,
		json.RawMessage(`["127.0.0.1"]`)))

	request := connect.NewRequest(&leapmuxv1.SetInitialSoloPasswordRequest{
		Password: "correct-horse-battery-staple",
	})
	request.Header().Set("Forwarded", "for=198.51.100.7;proto=https")
	request.Header().Set("User-Agent", "setup-browser")
	response, err := tcpAuthClient(base).SetInitialSoloPassword(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, usernames.Solo, response.Msg.GetUser().GetUsername())

	setCookie := response.Header().Get("Set-Cookie")
	// The trusted proxy verified HTTPS for this request, so the cookie is
	// Secure and takes the __Host- name without anybody setting
	// `secure_cookies`. The hub terminates no TLS itself, so the proxy's
	// answer is the only way it can know -- and an operator who had to set the
	// key by hand got a cookie with no Secure attribute until they did.
	assert.Contains(t, setCookie, "; Secure",
		"a trusted proxy that verified HTTPS turns the cookie policy on")
	assert.Contains(t, setCookie, "__Host-",
		"the secure policy also selects the __Host- prefixed name")
	cookieResponse := &http.Response{Header: http.Header{"Set-Cookie": []string{setCookie}}}
	cookies := cookieResponse.Cookies()
	require.Len(t, cookies, 1)
	session, err := srv.store.Sessions().GetByID(context.Background(), cookies[0].Value, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.7", session.IPAddress)
	assert.Equal(t, "setup-browser", session.UserAgent)

	adminRequest := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
	adminRequest.Header().Set("Cookie", cookies[0].Name+"="+cookies[0].Value)
	_, err = tcpClient(base).ListSettings(context.Background(), adminRequest)
	require.NoError(t, err, "the returned session must authorize the next protected RPC")

	_, err = tcpAuthClient(base).SetInitialSoloPassword(context.Background(), connect.NewRequest(
		&leapmuxv1.SetInitialSoloPasswordRequest{Password: "another-correct-password"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// The local IPC socket is the ONE credential-free transport, and the desktop
// app rests entirely on it: it reaches its own hub over that socket and has no
// way to present a password, so a hub that asked it for one would be unusable
// with no remedy inside the product.
//
// The rule is decided by auth.SoloGate and the mark is placed by the
// http.Server's BaseContext, which compares the accepting listener against
// localLn. Every other test of this case builds the marked context BY HAND, so
// all of them pass whatever that comparison does. This one drives real
// connections through the real server, which is the only way the wiring itself
// is under test.
//
// Both cases are asserted from ONE hub, after ONE password write, because the
// two answers have to diverge on the same process: a rule that refused both or
// admitted both would satisfy either case alone.
func TestServer_TheLocalSocketStaysCredentialFreeWhenTCPStopsBeing(t *testing.T) {
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	// Local IPC receives the synthetic account before setup. TCP can call only
	// the public initial-password procedure.
	require.NoError(t, listSettings(localIPCClient(t, srv)),
		"the local socket must admit the desktop app before a password exists")
	err := listSettings(tcpClient(base))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	setSoloPasswordDirect(t, srv.store)

	// TCP arms itself with no restart: the gate re-reads the account while its
	// latch is false.
	err = listSettings(tcpClient(base))
	require.Error(t, err, "TCP must ask for a sign-in once the account holds a password")
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// And the local socket does not, which is the whole point. A regression
	// here locks the desktop app out of its own hub.
	require.NoError(t, listSettings(localIPCClient(t, srv)),
		"the local socket is the one exception, and the desktop app cannot present a password")
}

// The login budget is keyed by the CALLER'S ADDRESS, and that address reaches
// the limiter only through the http.Server's ConnContext.
//
// ratelimit's own tests build the marked context by hand, so a missing
// ConnContext leaves every one of them green while every anonymous caller on
// the hub collapses into the single `anonymous:unknown` budget. The rate limit
// still fires, so a test that only counted refusals would not notice -- and the
// result is a hub where ten wrong passwords from anywhere stop everyone else
// signing in, which is worse than the unlimited Login this budget replaced.
//
// Two transports stand in for two addresses, because one machine has one usable
// loopback address on macOS. They land in different budgets when the stamp
// works and in the same one when it does not, which is the whole assertion.
func TestServer_TheLoginBudgetIsKeyedByTheCallersAddress(t *testing.T) {
	base := "127.0.0.1:" + strconv.Itoa(freePorts(t, 1)[0])
	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	overTCP := tcpAuthClient(base)
	overIPC := localIPCAuthClient(t, srv)

	// Spend the TCP address's whole budget: ten refused passwords.
	for i := range loginBudgetAttempts {
		err := attemptLogin(overTCP)
		require.Error(t, err, "attempt %d must be refused", i+1)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
			"a wrong password is a credential failure, attempt %d", i+1)
	}

	// The next one from THAT address is stopped by the budget rather than by
	// the password check.
	err := attemptLogin(overTCP)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"the eleventh attempt from one address must exhaust its budget")

	// A caller reaching the hub another way still has its own attempts. Without
	// the stamp it would be refused here by a budget it never spent.
	err = attemptLogin(overIPC)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"a second address must hold its own budget; a shared one locks out everybody")
}

// loginBudgetAttempts is the default rate_limit.login_anonymous allowance.
const loginBudgetAttempts = 10

// attemptLogin presents a password the solo account does not have.
func attemptLogin(c leapmuxv1connect.AuthServiceClient) error {
	_, err := c.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: usernames.Solo, Password: "not-the-password",
	}))
	return err
}

func tcpAuthClient(addr string) leapmuxv1connect.AuthServiceClient {
	return leapmuxv1connect.NewAuthServiceClient(
		&http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second},
		"http://"+addr)
}

func localIPCAuthClient(t *testing.T, srv *Server) leapmuxv1connect.AuthServiceClient {
	t.Helper()
	dial, err := locallisten.Dialer(srv.listenURL)
	require.NoError(t, err)
	return leapmuxv1connect.NewAuthServiceClient(
		&http.Client{
			Transport: &http.Transport{DialContext: locallisten.HTTPDialContext(dial)},
			Timeout:   10 * time.Second,
		},
		locallisten.LocalConnectURL)
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
