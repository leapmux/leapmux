package remoteipc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
	"github.com/leapmux/leapmux/locallisten"
)

// streamClientForHubURL returns an *http.Client + base URL (with
// transport selected appropriately for the hub address). Local-listen
// URLs (unix / npipe) are dialed via locallisten with an HTTP/1.1
// transport — the WebSocket upgrade handshake doesn't ride on HTTP/2
// streams. Remote URLs use an http2 transport against hubURL verbatim.
func streamClientForHubURL(hubURL string) (*http.Client, string) {
	return locallisten.SelectClient(
		hubURL,
		func() (*http.Client, string, error) { return locallisten.LocalHTTPClient(hubURL, 0) },
		func() (*http.Client, string) {
			return &http.Client{Transport: &http2.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			}}, hubURL
		},
	)
}

// HubEventStreamer streams server-side hub events to a worker-
// spawned CLI invocation through a per-user delegation bearer. The user-event subscription rides on the hub's
// `/ws/userevents` WebSocket; this streamer opens that WS upstream
// with the delegation bearer and forwards each frame to the local
// IPC consumer.
type HubEventStreamer struct {
	HubURL     string
	Delegation *crossworker.DelegationStore
	HTTPClient *http.Client
	ConnectURL string
}

// NewHubEventStreamer constructs a streamer scoped to a single
// user via Delegation.GetBearer.
func NewHubEventStreamer(hubURL string, delegation *crossworker.DelegationStore) *HubEventStreamer {
	httpClient, connectURL := streamClientForHubURL(hubURL)
	return &HubEventStreamer{
		HubURL:     hubURL,
		Delegation: delegation,
		HTTPClient: httpClient,
		ConnectURL: connectURL,
	}
}

// StreamHub satisfies HubStreamer. `WatchUser` is the only supported
// method: spawned-agent CLI invocations consume the user-scoped CRDT
// stream by tunneling `/ws/userevents` through the same delegation-
// token channel as unary calls.
func (s *HubEventStreamer) StreamHub(ctx context.Context, userID userid.UserID, method string, payload []byte, onPayload func([]byte) error) error {
	if s.Delegation == nil {
		return errors.New("remoteipc: delegation store not configured")
	}
	switch method {
	case "WatchUser":
		return s.watchUser(ctx, userID, payload, onPayload)
	default:
		return fmt.Errorf("remoteipc: hub stream method not implemented: %s", method)
	}
}

// watchUser opens `/ws/userevents` upstream with a fresh delegation
// bearer and forwards each binary WS frame as the protobuf payload
// of a marshalled WatchUserEvent. The framing format on /ws/userevents
// is `[4-byte big-endian length][protobuf WatchUserEvent]`; we strip
// the length prefix before re-emitting so the IPC consumer sees
// proto bytes directly.
func (s *HubEventStreamer) watchUser(ctx context.Context, userID userid.UserID, payload []byte, onPayload func([]byte) error) error {
	var req leapmuxv1.WatchUserRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("decode WatchUserRequest: %w", err)
	}
	bearer, err := s.Delegation.GetBearer(ctx, crossworker.DelegationScope{UserID: userID})
	if err != nil {
		return fmt.Errorf("delegation bearer: %w", err)
	}

	ws, err := channelwire.OpenUserEventsWS(ctx, s.HTTPClient, s.ConnectURL, bearer, req.GetWorkspaceIds(), nil, 0)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
	// Strip the 4-byte length prefix so the IPC consumer receives the
	// WatchUserEvent proto bytes directly.
	return channelwire.RunUserEventsReadLoop(ctx, ws, true, onPayload)
}
