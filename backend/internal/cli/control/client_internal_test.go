package control

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestClientServiceAccessors_KeepAuthenticationAndLargeResponses(t *testing.T) {
	// A valid unknown field works for every response type. Its wire size exceeds
	// ConnectRPC's default read limit, so this exercises each accessor's policy.
	body := protowire.AppendTag(nil, 50000, protowire.BytesType)
	body = protowire.AppendBytes(body, make([]byte, 5<<20))
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	hub := &HubClient{
		HTTPClient:      server.Client(),
		connectURL:      server.URL,
		bearer:          "test-token",
		accessExpiresAt: time.Now().Add(time.Hour),
	}
	ipc := &WorkerIPCClient{
		HTTPClient:       server.Client(),
		StreamHTTPClient: server.Client(),
		connectURL:       server.URL,
		token:            "test-token",
	}

	cases := []struct {
		procedure string
		workerIPC bool
		call      func(context.Context) (proto.Message, error)
	}{
		{procedure: leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.WorkspaceService().ListWorkspaces(ctx, &leapmuxv1.ListWorkspacesRequest{})
		}},
		{procedure: leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.WorkerManagementService().ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
		}},
		{procedure: leapmuxv1connect.AuthServiceGetCurrentUserProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AuthService().GetCurrentUser(ctx, &leapmuxv1.GetCurrentUserRequest{})
		}},
		{procedure: leapmuxv1connect.UserServiceListMyAPITokensProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.UserService().ListMyAPITokens(ctx, &leapmuxv1.ListMyAPITokensRequest{})
		}},
		{procedure: leapmuxv1connect.AdminSettingsServiceListSettingsProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AdminSettingsService().ListSettings(ctx, &leapmuxv1.ListSettingsRequest{})
		}},
		{procedure: leapmuxv1connect.AdminUserServiceListUsersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{})
		}},
		{procedure: leapmuxv1connect.AppServiceListAppsProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AppService().ListApps(ctx, &leapmuxv1.ListAppsRequest{})
		}},
		{procedure: leapmuxv1connect.AdminWorkerServiceListWorkersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AdminWorkerService().ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{})
		}},
		{procedure: leapmuxv1connect.AdminIdPServiceListOAuthProvidersProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AdminIdPService().ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{})
		}},
		{procedure: leapmuxv1connect.AdminNetworkServiceGetListenStatusProcedure, call: func(ctx context.Context) (proto.Message, error) {
			return hub.AdminNetworkService().GetListenStatus(ctx, &leapmuxv1.GetListenStatusRequest{})
		}},
		{procedure: leapmuxv1connect.ControlIPCServiceWhoamiProcedure, workerIPC: true, call: func(ctx context.Context) (proto.Message, error) {
			client, err := ipc.ControlIPCService()
			if err != nil {
				return nil, err
			}
			return client.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
		}},
		{procedure: leapmuxv1connect.ControlIPCServiceWhoamiProcedure, workerIPC: true, call: func(ctx context.Context) (proto.Message, error) {
			client, err := ipc.ControlIPCStreamService()
			if err != nil {
				return nil, err
			}
			return client.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.procedure, func(t *testing.T) {
			response, err := tc.call(t.Context())
			var request *http.Request
			select {
			case request = <-requests:
			default:
			}
			require.NoError(t, err, "every accessor must accept legitimate responses above the library default")
			require.NotNil(t, response)
			assert.Len(t, response.ProtoReflect().GetUnknown(), len(body))
			require.NotNil(t, request)
			assert.Equal(t, tc.procedure, request.URL.Path)
			if tc.workerIPC {
				assert.Equal(t, "test-token", request.Header.Get("X-LeapMux-Token"))
				assert.Empty(t, request.Header.Get("Authorization"))
			} else {
				assert.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
				assert.Empty(t, request.Header.Get("X-LeapMux-Token"))
			}
		})
	}
}

func TestHubClient_SetErrOutNilFallbackAndConcurrency(t *testing.T) {
	c := &HubClient{}
	assert.Same(t, os.Stderr, c.errWriter(), "unset error writer defaults to os.Stderr")

	custom := &bytes.Buffer{}
	c.SetErrOut(custom)
	assert.Same(t, custom, c.errWriter())

	c.SetErrOut(nil)
	assert.Same(t, os.Stderr, c.errWriter(), "passing nil resets the error writer to os.Stderr")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			local := &bytes.Buffer{}
			for j := range 50 {
				if (i+j)%2 == 0 {
					c.SetErrOut(local)
				} else {
					c.SetErrOut(nil)
				}
				assert.NotNil(t, c.errWriter())
			}
		})
	}
	wg.Wait()
}

func TestWorkerIPCClient_ControlIPCServiceNilHTTPClientAndAuth(t *testing.T) {
	empty := &WorkerIPCClient{}

	unary, err := empty.ControlIPCService()
	require.Error(t, err)
	assert.Nil(t, unary)
	assert.Contains(t, err.Error(), "no HTTP client")

	stream, err := empty.ControlIPCStreamService()
	require.Error(t, err)
	assert.Nil(t, stream)
	assert.Contains(t, err.Error(), "no HTTP client")

	headers := http.Header{}
	empty.ApplyAuth(headers)
	assert.Empty(t, headers.Get("X-LeapMux-Token"), "empty token must not stamp the IPC header")

	ipc := &WorkerIPCClient{token: "ipc-secret"}
	var gotHeader string
	next := func(ctx context.Context, _ connect.Spec) (connect.ClientStream, error) {
		info, ok := connect.CallInfoForClientContext(ctx)
		require.True(t, ok, "interceptor must attach CallInfo when absent")
		gotHeader = info.RequestHeader().Get("X-LeapMux-Token")
		return &stubReplayClientStream{}, nil
	}
	wrapped := ipc.AuthInterceptor()(next)

	// Without an existing CallInfo in context.
	_, err = wrapped(t.Context(), connect.Spec{StreamType: connect.StreamTypeUnary})
	require.NoError(t, err)
	assert.Equal(t, "ipc-secret", gotHeader)

	// With an existing CallInfo in context, existing headers stay intact.
	ctx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("X-Custom", "keep")
	_, err = wrapped(ctx, connect.Spec{StreamType: connect.StreamTypeServer})
	require.NoError(t, err)
	assert.Equal(t, "ipc-secret", info.RequestHeader().Get("X-LeapMux-Token"))
	assert.Equal(t, "keep", info.RequestHeader().Get("X-Custom"))
}

func TestUnaryReplayStream_CloseBeforeOpenAndNilStream(t *testing.T) {
	empty := &unaryReplayStream{}
	assert.NoError(t, empty.Close(), "closing a replay stream with a nil underlying stream must not panic")

	ctx, info := connect.NewClientContext(t.Context())
	refusal := connect.NewError(connect.CodeUnauthenticated, "expired")
	first := &stubReplayClientStream{
		receive: func(any) error { return refusal },
	}
	opens := 0
	var s *unaryReplayStream
	s = &unaryReplayStream{
		ctx:    ctx,
		info:   info,
		stream: first,
		repairs: []unaryRepair{{
			matches: func(err error, _ *connect.Header) bool { return connect.CodeOf(err) == connect.CodeUnauthenticated },
			repair: func(context.Context) error {
				// Simulate Close arriving while repair is in progress, before open runs.
				require.NoError(t, s.Close())
				return nil
			},
		}},
		open: func() (connect.ClientStream, error) {
			opens++
			return &stubReplayClientStream{}, nil
		},
	}

	err := s.Receive(&leapmuxv1.GetCurrentUserResponse{})
	require.ErrorIs(t, err, refusal)
	assert.Zero(t, opens, "open must not run after Close already ran")
	assert.GreaterOrEqual(t, first.closed.Load(), int32(1))
}

func TestAuthInterceptor_RepairFailureAttachesHintAndUnwrapsBoth(t *testing.T) {
	t.Run("elevation unsupported attaches repair failure hint", func(t *testing.T) {
		hub := newRepairHub(t, 3600, elevationRequired())
		hub.elevation.startStatus = http.StatusBadRequest
		seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))

		c, err := NewClient(hub.server.URL)
		require.NoError(t, err)
		c.promptAllowed = true
		captureClientErr(c)

		err = hub.call(c)
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.ErrorIs(t, err, ErrElevationUnsupported)
		classified := cli.FromError(err)
		assert.Contains(t, strings.Join(classified.Hints, "\n"), "automatic session repair failed")
		assert.Contains(t, strings.Join(classified.Hints, "\n"), ErrElevationUnsupported.Error())
	})

	t.Run("non-terminal stdin without env var states terminal reason", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv(noPromptEnv, "")

		mux := http.NewServeMux()
		mux.HandleFunc(leapmuxv1connect.UserServiceListMyAPITokensProcedure, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(ElevationRequiredHeader, "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"verify identity"}`))
		})
		srv := hubtransporttest.NewServer(t, mux)
		t.Cleanup(srv.Close)

		require.NoError(t, SaveCredentials(srv.URL, CredentialFile{
			HubURL:      srv.URL,
			AccessToken: "lmx_elev_tty",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
		c, err := NewClient(srv.URL)
		require.NoError(t, err)
		c.promptAllowed = false

		_, err = c.UserService().ListMyAPITokens(t.Context(), &leapmuxv1.ListMyAPITokensRequest{})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrElevationNeedsAPerson)
		classified := cli.FromError(err)
		assert.Contains(t, strings.Join(classified.Hints, "\n"), "stdin is not a terminal")
	})

	t.Run("hintedError preserves repairErr when err is nil and vice versa", func(t *testing.T) {
		rpcErr := connect.NewError(connect.CodeUnauthenticated, "expired")
		onlyErr := &hintedError{err: rpcErr}
		assert.Equal(t, []error{rpcErr}, onlyErr.Unwrap())
		assert.Nil(t, onlyErr.CLIHints())

		onlyRepair := &hintedError{repairErr: ErrCredentialRejected, hint: "repair failed"}
		assert.Equal(t, "", onlyRepair.Error())
		assert.Equal(t, []error{ErrCredentialRejected}, onlyRepair.Unwrap())
		assert.Equal(t, []string{"repair failed"}, onlyRepair.CLIHints())
		assert.True(t, errors.Is(onlyRepair, ErrCredentialRejected))
	})
}

func TestWrapHubConnectionError_ClassifiesTooManyConnections(t *testing.T) {
	t.Run("wraps CloseReasonTooManyConnections as retryable CodeUnavailable", func(t *testing.T) {
		raw := websocket.CloseError{
			Code:   websocket.StatusPolicyViolation,
			Reason: contracts.CloseReasonTooManyConnections,
		}
		wrapped := wrapHubConnectionError(raw)
		require.Error(t, wrapped)

		var closeErr websocket.CloseError
		require.ErrorAs(t, wrapped, &closeErr)
		assert.Equal(t, contracts.CloseReasonTooManyConnections, closeErr.Reason)

		cliErr := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeUnavailable, cliErr.Code)
		assert.True(t, cliErr.Retryable())
		assert.Equal(t, "the Hub rejected the connection: too many open connections", cliErr.Message)
	})

	t.Run("leaves other close reasons untouched", func(t *testing.T) {
		raw := websocket.CloseError{
			Code:   websocket.StatusPolicyViolation,
			Reason: contracts.CloseReasonForbidden,
		}
		wrapped := wrapHubConnectionError(raw)
		assert.Equal(t, raw, wrapped)
		assert.Equal(t, cli.CodeInternal, cli.FromError(wrapped).Code)
	})

	t.Run("UserEventsStream Recv classifies too_many_connections from live server", func(t *testing.T) {
		gotTopics := make(chan []leapmuxv1.UserEventTopic, 1)
		srv := hubtransporttest.NewHTTP1Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			topics, err := channelwire.ParseUserEventsTopicsFromQuery(r.URL.Query())
			if err == nil {
				gotTopics <- topics
			}
			ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
			})
			if err != nil {
				return
			}
			_ = ws.Close(websocket.StatusPolicyViolation, contracts.CloseReasonTooManyConnections)
		}))
		t.Cleanup(srv.Close)

		c := &HubClient{
			HubURL:          srv.URL,
			WSClient:        srv.Client(),
			connectURL:      srv.URL,
			bearer:          "test-token",
			accessExpiresAt: time.Now().Add(time.Hour),
		}
		stream, err := c.OpenUserEventsWithOptions(t.Context(), channelwire.UserEventsOptions{
			Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS},
		})
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()

		_, err = stream.Recv()
		require.Error(t, err)
		cliErr := cli.FromError(err)
		assert.Equal(t, cli.CodeUnavailable, cliErr.Code)
		assert.True(t, cliErr.Retryable())
		assert.Equal(t, "the Hub rejected the connection: too many open connections", cliErr.Message)
		assert.Equal(t, []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS}, <-gotTopics)
	})
}
