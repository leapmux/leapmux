package controlipc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

// HubEventStreamer streams server-side hub events to a worker-
// spawned CLI invocation through a per-user delegation bearer. The user-event subscription rides on the hub's
// `/ws/userevents` WebSocket; this streamer opens that WS upstream
// with the delegation bearer and forwards each frame to the local
// IPC consumer.
type HubEventStreamer struct {
	HubURL string
	// Delegation is the interface, not the concrete store, for the same
	// reason HubUnaryBridge takes it: this type calls GetBearer and nothing
	// else, and a test needs to supply that one method.
	Delegation crossworker.DelegationProvider
	HTTPClient *http.Client
	ConnectURL string
}

// NewHubEventStreamer constructs a streamer scoped to a single
// user via Delegation.GetBearer.
//
// It takes the WebSocket client, which is HTTP/1.1 always. A WebSocket cannot
// ride HTTP/2 — coder/websocket needs http.Hijacker, which the HTTP/2
// ResponseWriter does not implement — so this lane must not share the unary
// bridge's client. It also carries NO overall timeout: an http.Client timeout
// covers the body read, and this body ends only when the subscription does.
func NewHubEventStreamer(endpoint *hubtransport.Endpoint, delegation crossworker.DelegationProvider) *HubEventStreamer {
	return &HubEventStreamer{
		HubURL:     endpoint.URL(),
		Delegation: delegation,
		HTTPClient: endpoint.WebSocketClient(),
		ConnectURL: endpoint.BaseURL(),
	}
}

// StreamHub satisfies HubStreamer. `WatchUser` is the only supported
// method: spawned-agent CLI invocations consume the user-scoped CRDT
// stream by tunneling `/ws/userevents` through the same delegation-
// token channel as unary calls.
func (s *HubEventStreamer) StreamHub(ctx context.Context, userID userid.UserID, method string, payload []byte, onPayload func([]byte) error) error {
	if s.Delegation == nil {
		return errors.New("controlipc: delegation store not configured")
	}
	switch method {
	case "WatchUser":
		return s.watchUser(ctx, userID, payload, onPayload)
	default:
		return fmt.Errorf("controlipc: hub stream method not implemented: %s", method)
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
