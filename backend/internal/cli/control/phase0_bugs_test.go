package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	// WRAPPERS-3: errors.Is must still match the underlying repair sentinel.
	assert.ErrorIs(t, err, ErrElevationNeedsAPerson)
}

func TestHintedError_NilGuardAndUnwrapBothErrors(t *testing.T) {
	var empty hintedError
	assert.NotPanics(t, func() {
		assert.Equal(t, "", empty.Error())
		assert.Nil(t, empty.CLIHints())
		assert.Nil(t, empty.Unwrap())
	})
	var nilPtr *hintedError
	assert.NotPanics(t, func() {
		assert.Equal(t, "", nilPtr.Error())
		assert.Nil(t, nilPtr.CLIHints())
		assert.Nil(t, nilPtr.Unwrap())
	})

	rpcErr := connect.NewError(connect.CodeUnauthenticated, "token expired")
	for _, sentinel := range []error{
		ErrCredentialRejected,
		ErrElevationNeedsAPerson,
		ErrElevationUnsupported,
		context.Canceled,
	} {
		he := &hintedError{
			err:       rpcErr,
			repairErr: sentinel,
			hint:      "automatic session repair failed",
		}
		assert.ErrorIs(t, he, rpcErr)
		assert.ErrorIs(t, he, sentinel)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(he))
		wrapped := fmt.Errorf("locate tab t1: %w", he)
		classified := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeNotLoggedIn, classified.Code)
		assert.Equal(t, "locate tab t1: token expired", classified.Message)
		assert.Equal(t, []string{"automatic session repair failed"}, classified.Hints)
		assert.ErrorIs(t, classified, sentinel)
	}
}

type stubReplayClientStream struct {
	receive func(any) error
	closed  atomic.Int32
}

func (s *stubReplayClientStream) SendHeaders() error { return nil }
func (s *stubReplayClientStream) Send(any) error     { return nil }
func (s *stubReplayClientStream) CloseSend() error   { return nil }
func (s *stubReplayClientStream) Receive(res any) error {
	if s.receive != nil {
		return s.receive(res)
	}
	return nil
}
func (s *stubReplayClientStream) Close() error {
	s.closed.Add(1)
	return nil
}

// WRAPPERS-1: Close running concurrently with Receive's replay open must not
// race on s.stream and must close the newly opened stream before returning.
func TestUnaryReplayStream_ConcurrentCloseAndReceiveIsRaceFree(t *testing.T) {
	ctx, info := connect.NewClientContext(t.Context())
	openStarted := make(chan struct{})
	closeDone := make(chan struct{})
	first := &stubReplayClientStream{
		receive: func(any) error {
			return connect.NewError(connect.CodeUnauthenticated, "expired")
		},
	}
	second := &stubReplayClientStream{}

	s := &unaryReplayStream{
		ctx:    ctx,
		info:   info,
		stream: first,
		repairs: []unaryRepair{
			{
				matches: func(err error, _ *connect.Header) bool {
					return connect.CodeOf(err) == connect.CodeUnauthenticated
				},
				repair: func(context.Context) error { return nil },
			},
		},
		open: func() (connect.ClientStream, error) {
			close(openStarted)
			<-closeDone
			return second, nil
		},
	}

	var wg sync.WaitGroup
	var recvErr error
	wg.Go(func() {
		recvErr = s.Receive(&leapmuxv1.GetCurrentUserResponse{})
	})

	<-openStarted
	require.NoError(t, s.Close())
	close(closeDone)
	wg.Wait()

	require.Error(t, recvErr)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(recvErr))
	assert.GreaterOrEqual(t, first.closed.Load(), int32(1), "first stream must be closed")
	assert.Equal(t, int32(1), second.closed.Load(), "replay stream opened concurrently with Close must be closed immediately")
}

func TestAuthInterceptor_NothingToRefreshDoesNotAttachRepairFailureHint(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc(leapmuxv1connect.AuthServiceGetCurrentUserProcedure, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"login required"}`))
	})
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	// Anonymous client has no credential to refresh (repair returns errNothingToRefresh).
	c, err := NewClientOrAnonymous(srv.URL)
	require.NoError(t, err)

	_, err = c.AuthService().GetCurrentUser(t.Context(), &leapmuxv1.GetCurrentUserRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	var he *hintedError
	assert.False(t, errors.As(err, &he), "errNothingToRefresh must return the hub error directly without a repair-failed hint")
	classified := cli.FromError(err)
	assert.NotContains(t, strings.Join(classified.Hints, "\n"), "automatic session repair failed")
}
