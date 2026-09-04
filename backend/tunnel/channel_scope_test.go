package tunnel_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/tunnel"
)

// The scope model, end to end, through a real Noise channel.
//
// This is the test the whole worker-enforcement argument rests on. Everything
// else about scopes is checked at the Hub, which is the wrong layer to prove
// it: an inner RPC travels ENCRYPTED from the browser to the worker, and the
// Hub relays bytes it cannot read. So a scope that bound only at the Hub would
// bind on nothing that matters here, and every unit test would still pass.
//
// It runs against a SOLO hub, which is also the point. The solo rung yields
// when an lmx_ bearer is presented, so a solo user can hand an app a narrow
// credential and have the narrowing actually take effect.
func TestChannelScopeBindsInsideTheNoiseSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	// A credential granted file:read and NOTHING else, through the real
	// device-code flow -- so what binds below is what a consent screen would
	// actually produce, not a row a test wrote by hand.
	bearer, granted := authorizeScopedCredential(t, httpClient, hubURL, "file:read")
	// The closure widens along its OWN implications and no further: file:read
	// implies worker:read, because reading a file means reaching the machine
	// that holds it. It must not reach terminal:write, which is what the
	// refusal below then measures.
	//
	// The mint response IS the stored grant -- the closure runs at the mint, so
	// the row, the consent screen and this string are one value.
	assert.Contains(t, granted, "worker:read", "file:read implies reaching the machine")
	assert.NotContains(t, granted, "terminal:write", "asking for file:read must not grant a terminal write")

	ch, err := tunnel.OpenChannel(ctx, hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext: ctx,
		BearerToken:     bearer,
	})
	require.NoError(t, err, "a scoped credential must still open a channel")
	t.Cleanup(ch.Close)

	// What it WAS granted reaches the handler.
	//
	// A file that does not exist, on purpose: the answer must come from INSIDE
	// ReadFile, and "file not found" can only be written after the scope gate
	// admitted the call. A real file would prove the same thing and would also
	// depend on what happens to be on the machine running the test.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	readPayload, err := proto.Marshal(&leapmuxv1.ReadFileRequest{
		Path: filepath.Join(home, "leapmux-scope-test-does-not-exist"),
	})
	require.NoError(t, err)
	// CallRPC surfaces the inner error as a Go error, so the assertion reads
	// err rather than the response.
	_, err = ch.CallRPC(ctx, "ReadFile", readPayload)
	require.Error(t, err, "the file does not exist, so the handler must say so")
	assert.Contains(t, err.Error(), "not found",
		"file:read must reach the handler, which then fails on its own argument")
	assert.NotContains(t, err.Error(), "file:read",
		"a granted permission must never produce a scope refusal")

	// What it was NOT granted is refused INSIDE the session. The Hub relayed
	// these bytes without being able to read them, so this refusal can only
	// have come from the worker.
	inputPayload, err := proto.Marshal(&leapmuxv1.SendInputRequest{
		TerminalId: "t-does-not-exist", Data: []byte("echo hi\n"),
	})
	require.NoError(t, err)
	_, err = ch.CallRPC(ctx, "SendInput", inputPayload)
	require.Error(t, err, "terminal:write was not granted, so SendInput must be refused")
	assert.Contains(t, err.Error(), "terminal:write",
		"the refusal names the permission, so an app operator can ask for it")

	// The CHANNEL survives the refusal: a scope denial is one call's answer,
	// not a transport fault. Without this the assertion above would also pass
	// for a worker that tore the session down.
	_, err = ch.CallRPC(ctx, "ReadFile", readPayload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found", "the channel stays usable after a refusal")

	// The refusal is about the SCOPE, not about the missing terminal: a
	// credential that holds terminal:write reaches the handler and fails on the
	// id instead. Without this the assertion above would pass for an
	// unimplemented method.
	wide, _ := authorizeScopedCredential(t, httpClient, hubURL, "file:read terminal:write")
	wideCh, err := tunnel.OpenChannel(ctx, hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext: ctx,
		BearerToken:     wide,
	})
	require.NoError(t, err)
	t.Cleanup(wideCh.Close)

	_, err = wideCh.CallRPC(ctx, "SendInput", inputPayload)
	require.Error(t, err, "the terminal id is still bogus")
	assert.NotContains(t, err.Error(), "terminal:write",
		"a granted credential must reach the handler and fail on its own arguments")
}

// authorizeScopedCredential runs the device-code flow against a solo hub and
// returns the access token it mints.
//
// The DEVICE flow, not a hand-written row: it is the one leg whose consent a
// test can complete without a browser, and driving the real endpoints is what
// makes the grant below the same value a consent screen produces.
func authorizeScopedCredential(t *testing.T, client *http.Client, hubURL, scope string) (bearer string, granted []string) {
	t.Helper()

	postForm := func(path string, form url.Values) map[string]any {
		t.Helper()
		resp, err := client.PostForm(hubURL+path, form)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d: %s", path, resp.StatusCode, raw)
		}
		var body map[string]any
		require.NoErrorf(t, json.Unmarshal(raw, &body), "%s answered %s", path, raw)
		return body
	}

	grant := postForm("/oauth/device-authorization", url.Values{
		"client_id":         {oauthapp.ControlCLIClientID},
		"installation_name": {"scope-test"},
		"scope":             {scope},
	})
	userCode, _ := grant["user_code"].(string)
	deviceCode, _ := grant["device_code"].(string)
	require.NotEmpty(t, userCode)
	require.NotEmpty(t, deviceCode)

	// The consent POST uses the elevated session from first-password setup.
	approve, err := client.PostForm(hubURL+"/oauth/device", url.Values{
		"user_code": {userCode}, "decision": {"allow"},
	})
	require.NoError(t, err)
	defer func() { _ = approve.Body.Close() }()
	require.Equal(t, http.StatusOK, approve.StatusCode)

	tokens := postForm("/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	})
	access, _ := tokens["access_token"].(string)
	require.True(t, strings.HasPrefix(access, "lmx_"), "the mint must answer a bearer")

	// The grant that came back COVERS what was asked, so a later refusal is
	// about the scope rather than about a mint that quietly narrowed it.
	//
	// It is a superset rather than an equal, and the difference is the scope
	// CLOSURE. Asking for file:read implies worker:read, because reading a file
	// means reaching the machine that holds it -- a grant that admitted the
	// first and refused the second would describe an app that cannot open the
	// channel it needs. The closure runs at the mint, so the stored grant, the
	// consent screen and this response all show the same set.
	grantedRaw, _ := tokens["scope"].(string)
	granted = strings.Fields(grantedRaw)
	for _, asked := range strings.Fields(scope) {
		assert.Containsf(t, granted, asked, "the mint dropped %s from what the consent granted", asked)
	}
	return access, granted
}

// The UNSCOPED pole of the wire field, through a real hub.
//
// granted_scopes is what the Hub announces on ChannelOpenRequest, and the
// worker reads it into Caller.Scopes. SCOPE_ALL is the explicit absence of a
// limit, which is what a session carries -- so a session must meet no refusal
// the scope model owns, on any method.
//
// The OTHER pole, an empty list, is checked at the Manager instead:
// channel.TestHandleOpen_RefusesAnAnnouncedGrantOfNothing. A hub never sends
// one, so a test that drove a hub could not produce it -- which is the point of
// the refusal, and the reason it needs a test that can construct the request
// directly.
func TestChannelOpenReadsTheAnnouncedGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	hubURL, _, _, workerID, httpClient := startTestSolo(t)
	ctx := context.Background()

	// The session from first-password setup is unscoped. It reaches every
	// method, which is what SCOPE_ALL means.
	ch, err := tunnel.OpenChannel(ctx, hubURL, workerID, &tunnel.OpenChannelOptions{
		LifetimeContext:     ctx,
		HTTPClient:          httpClient,
		WebSocketHTTPClient: httpClient,
	})
	require.NoError(t, err)
	t.Cleanup(ch.Close)

	inputPayload, err := proto.Marshal(&leapmuxv1.SendInputRequest{
		TerminalId: "t-does-not-exist", Data: []byte("echo hi\n"),
	})
	require.NoError(t, err)
	_, err = ch.CallRPC(ctx, "SendInput", inputPayload)
	require.Error(t, err, "the terminal id is bogus")
	assert.NotContains(t, err.Error(), "permission",
		"an unscoped caller must be refused by nothing the scope model owns")
}
