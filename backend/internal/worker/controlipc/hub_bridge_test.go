package controlipc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

// These cases cover the reported failure directly: inside a spawned agent,
// `leapmux control tab list`, `worker list` and `agent send` all died with
// "http2: unencrypted HTTP/2 not enabled" against a plaintext hub, while
// `whoami` -- which the worker answers itself -- kept working.
//
// Every hub-bound call from a spawned agent goes through one of the two types
// here, so a plaintext hub and an HTTP/1.1-only hub are both exercised end to
// end, over the real wire protocol.

const testBearer = "delegation-bearer-for-u-1"

// stubDelegation supplies the per-user bearer the bridges present upstream.
type stubDelegation struct{ err error }

func (s stubDelegation) GetBearer(context.Context, crossworker.DelegationScope) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return testBearer, nil
}

// fakeWorkspaceService answers ListTabs and records the credential and the
// HTTP version it saw.
//
// gotProto records `r.Proto` and NOT `req.Peer().Protocol`, the way
// userEventsHandler does. Peer().Protocol is the RPC protocol token
// ("grpc"/"connect"), which reads the same against an h2c hub and an
// HTTP/1.1-only one -- so it cannot tell whether the unary lane took the h2c
// preference this package exists to provide.
type fakeWorkspaceService struct {
	leapmuxv1connect.UnimplementedWorkspaceServiceHandler
	gotAuth  string
	gotProto string
}

func (f *fakeWorkspaceService) ListTabs(ctx context.Context, req *connect.Request[leapmuxv1.ListTabsRequest]) (*connect.Response[leapmuxv1.ListTabsResponse], error) {
	f.gotAuth = req.Header().Get("Authorization")
	if proto, ok := requestProtoFrom(ctx); ok {
		f.gotProto = proto
	}
	return connect.NewResponse(&leapmuxv1.ListTabsResponse{
		Tabs: []*leapmuxv1.WorkspaceTab{{TabId: "t-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
	}), nil
}

// requestProtoKey carries the HTTP version down to the handler. ConnectRPC
// hands a handler no *http.Request, and Peer().Protocol answers a different
// question, so the surrounding mux records it.
type requestProtoKey struct{}

func requestProtoFrom(ctx context.Context) (string, bool) {
	proto, ok := ctx.Value(requestProtoKey{}).(string)
	return proto, ok
}

// recordRequestProto wraps a handler so the request's HTTP version reaches it.
func recordRequestProto(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestProtoKey{}, r.Proto)))
	})
}

// userEventsHandler serves /ws/userevents: it records the credential from the
// upgrade and writes one length-prefixed WatchUserEvent.
type userEventsHandler struct {
	gotAuth  chan string
	gotProto chan string
}

func newUserEventsHandler() *userEventsHandler {
	return &userEventsHandler{gotAuth: make(chan string, 1), gotProto: make(chan string, 1)}
}

func (h *userEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case h.gotAuth <- r.Header.Get("Authorization"):
	default:
	}
	select {
	case h.gotProto <- r.Proto:
	default:
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusInternalError, "") }()

	event, err := proto.Marshal(&leapmuxv1.WatchUserEvent{})
	if err != nil {
		return
	}
	if err := channelwire.WriteFramedBytes(r.Context(), conn, event); err != nil {
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// startHub serves the WorkspaceService and /ws/userevents on one listener,
// the way the real hub does. newServer picks the protocols it offers.
func startHub(t *testing.T, newServer func(*testing.T, http.Handler) *httptest.Server) (string, *fakeWorkspaceService, *userEventsHandler) {
	t.Helper()
	workspaces := &fakeWorkspaceService{}
	events := newUserEventsHandler()
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewWorkspaceServiceHandler(workspaces)
	mux.Handle(path, handler)
	mux.Handle(contracts.WSRouteUserEvents, events)
	// The unary handler is wrapped so it sees the request's HTTP version; the
	// WebSocket handler reads r.Proto itself.
	return newServer(t, recordRequestProto(mux)).URL, workspaces, events
}

func mustEndpoint(t *testing.T, url string) *hubtransport.Endpoint {
	t.Helper()
	endpoint, err := hubtransport.New(url)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	return endpoint
}

// TestHubUnaryBridge_ReachesAPlaintextHub is THE regression test for the
// reported bug. Before the fix, CallHub failed with "http2: unencrypted
// HTTP/2 not enabled" before a single byte reached the network.
func TestHubUnaryBridge_ReachesAPlaintextHub(t *testing.T) {
	hubURL, workspaces, _ := startHub(t, hubtransporttest.NewServer)
	bridge := controlipc.NewHubUnaryBridge(mustEndpoint(t, hubURL), stubDelegation{})

	payload, err := proto.Marshal(&leapmuxv1.ListTabsRequest{})
	require.NoError(t, err)
	out, err := bridge.CallHub(context.Background(), userid.MustNew("u-1"), "ListTabs", payload)
	require.NoError(t, err)

	var resp leapmuxv1.ListTabsResponse
	require.NoError(t, proto.Unmarshal(out, &resp))
	require.Len(t, resp.GetTabs(), 1)
	assert.Equal(t, "t-1", resp.GetTabs()[0].GetTabId())
	assert.Equal(t, "Bearer "+testBearer, workspaces.gotAuth, "the delegation bearer must reach the hub")
	// The h2c PREFERENCE, which is the whole reason this lane exists: against a
	// hub that speaks it, the unary call must arrive over HTTP/2 and not fall
	// back. Peer().Protocol answers "connect" for both hubs and cannot say this.
	assert.Equal(t, "HTTP/2.0", workspaces.gotProto, "a hub that speaks h2c must be reached over h2c")
}

// TestHubUnaryBridge_ReachesAnHTTP11OnlyHub covers the same call against a
// hub behind a reverse proxy with no h2c: the lane falls back rather than
// failing, because a unary Connect call works over HTTP/1.1.
func TestHubUnaryBridge_ReachesAnHTTP11OnlyHub(t *testing.T) {
	hubURL, workspaces, _ := startHub(t, hubtransporttest.NewHTTP1Server)
	bridge := controlipc.NewHubUnaryBridge(mustEndpoint(t, hubURL), stubDelegation{})

	payload, err := proto.Marshal(&leapmuxv1.ListTabsRequest{})
	require.NoError(t, err)
	_, err = bridge.CallHub(context.Background(), userid.MustNew("u-1"), "ListTabs", payload)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+testBearer, workspaces.gotAuth)
	assert.Equal(t, "HTTP/1.1", workspaces.gotProto, "the fallback is the point of this case")
}

// TestHubEventStreamer_ReachesAPlaintextHub covers the streaming half. It
// failed for a second reason: the streamer shared the unary client, and a
// WebSocket upgrade cannot ride an HTTP/2 connection at all -- so `WatchUser`
// was broken against every remote hub, `https://` included.
func TestHubEventStreamer_ReachesAPlaintextHub(t *testing.T) {
	for name, newServer := range map[string]func(*testing.T, http.Handler) *httptest.Server{
		"h2c hub":           hubtransporttest.NewServer,
		"HTTP/1.1-only hub": hubtransporttest.NewHTTP1Server,
	} {
		t.Run(name, func(t *testing.T) {
			hubURL, _, events := startHub(t, newServer)
			streamer := controlipc.NewHubEventStreamer(mustEndpoint(t, hubURL), stubDelegation{})

			payload, err := proto.Marshal(&leapmuxv1.WatchUserRequest{})
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			frames := make(chan []byte, 4)
			err = streamer.StreamHub(ctx, userid.MustNew("u-1"), "WatchUser", payload, func(p []byte) error {
				frames <- append([]byte(nil), p...)
				return nil
			})
			require.NoError(t, err)

			require.Len(t, frames, 1)
			// The payload reaches the consumer with the 4-byte length prefix
			// already stripped, so it unmarshals as the event itself.
			var event leapmuxv1.WatchUserEvent
			require.NoError(t, proto.Unmarshal(<-frames, &event))

			assert.Equal(t, "Bearer "+testBearer, <-events.gotAuth, "the upgrade must carry the delegation bearer")
			assert.Equal(t, "HTTP/1.1", <-events.gotProto, "a WebSocket upgrade cannot ride HTTP/2")
		})
	}
}

// TestHubBridgesDoNotShareOneClient pins the split that the two lanes need.
//
// One shared client is what the bug was: the WebSocket lane forced an HTTP/2
// transport onto the unary lane, and neither lane could hold the timeout it
// needs -- a unary call must have one so a silent hub cannot hang an agent's
// `tab list` for ever, and a subscription must NOT, because the client timeout
// covers the body read and would end the stream on a clock.
func TestHubBridgesDoNotShareOneClient(t *testing.T) {
	endpoint := mustEndpoint(t, "http://hub.invalid:4327")
	bridge := controlipc.NewHubUnaryBridge(endpoint, stubDelegation{})
	streamer := controlipc.NewHubEventStreamer(endpoint, stubDelegation{})

	assert.NotSame(t, bridge.HTTPClient, streamer.HTTPClient)
	assert.NotZero(t, bridge.HTTPClient.Timeout, "a unary hub call must be bounded")
	assert.Zero(t, streamer.HTTPClient.Timeout, "a subscription must not be bounded by a client timeout")
	assert.Equal(t, endpoint.BaseURL(), bridge.ConnectURL)
	assert.Equal(t, endpoint.BaseURL(), streamer.ConnectURL)
}
