package control

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
)

// B11: HubHost must distinguish schemes (http vs https) and base paths so
// credentials for two distinct hubs never overwrite each other.
func TestHubHost_DistinguishesSchemeAndBasePath(t *testing.T) {
	httpHost, err := HubHost("http://hub.example.com")
	require.NoError(t, err)
	httpsHost, err := HubHost("https://hub.example.com")
	require.NoError(t, err)
	assert.NotEqual(t, httpHost, httpsHost, "http and https origins must not share a credential file")

	rootHost, err := HubHost("https://hub.example.com")
	require.NoError(t, err)
	pathHost, err := HubHost("https://hub.example.com/team-a")
	require.NoError(t, err)
	assert.NotEqual(t, rootHost, pathHost, "distinct base paths must not share a credential file")
}

// B07: OpenUserEvents must work over a unix:/npipe: Hub socket URL via
// hubtransport.Endpoint's WebSocketClient and BaseURL.
func TestOpenUserEvents_SupportsLocalSocketHub(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

	upgraded := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(contracts.WSRouteUserEvents, func(w http.ResponseWriter, r *http.Request) {
		upgraded <- r.Header.Get("Authorization")
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
		})
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		<-r.Context().Done()
	})
	sockURL := hubtransporttest.NewSocketServer(t, "cliuserevents", mux)

	require.NoError(t, SaveCredentials(sockURL, CredentialFile{
		HubURL:      sockURL,
		AccessToken: "lmx_sock_token",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserID:      "u-1",
		Username:    "alice",
	}))
	c, err := NewClient(sockURL)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := c.OpenUserEvents(ctx, nil)
	require.NoError(t, err, "OpenUserEvents must work over a unix:/npipe: hub socket")
	defer func() { _ = stream.Close() }()

	select {
	case gotAuth := <-upgraded:
		assert.Equal(t, "Bearer lmx_sock_token", gotAuth)
	case <-ctx.Done():
		t.Fatal("timed out waiting for WebSocket upgrade over local socket")
	}
}

// B13: When step-up elevation cannot prompt (non-interactive or
// LEAPMUX_CONTROL_NO_PROMPT), the returned error must keep the Hub's refusal
// AND surface why the prompt could not run as a hint.
func TestAuthInterceptor_ElevationBlockedSurfacesPromptReason(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	t.Setenv(noPromptEnv, "1")

	mux := http.NewServeMux()
	mux.HandleFunc(leapmuxv1connect.UserServiceListMyAPITokensProcedure, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ElevationRequiredHeader, "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"recent identity verification required"}`))
	})
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	require.NoError(t, SaveCredentials(srv.URL, CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_elev_test",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	c, err := NewClient(srv.URL)
	require.NoError(t, err)

	_, err = c.UserService().ListMyAPITokens(t.Context(), &leapmuxv1.ListMyAPITokensRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	classified := cli.FromError(err)
	assert.Contains(t, classified.Message, "recent identity verification required")
	joinedHints := strings.Join(classified.Hints, "\n")
	assert.Contains(t, joinedHints, "LEAPMUX_CONTROL_NO_PROMPT", "hint must explain why the verification prompt did not run")
}
