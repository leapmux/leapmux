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

// HubWorkspaceStreamer streams server-side hub events to a worker-
// spawned CLI invocation through a per-(user, workspace) delegation
// bearer. The org-event subscription rides on the hub's
// `/ws/orgevents` WebSocket; this streamer opens that WS upstream
// with the delegation bearer and forwards each frame to the local
// IPC consumer.
type HubWorkspaceStreamer struct {
	HubURL     string
	Delegation *crossworker.DelegationStore
	HTTPClient *http.Client
	ConnectURL string
}

// NewHubWorkspaceStreamer constructs a streamer scoped to a single
// (user, workspace) pair via Delegation.GetBearer.
func NewHubWorkspaceStreamer(hubURL string, delegation *crossworker.DelegationStore) *HubWorkspaceStreamer {
	httpClient, connectURL := streamClientForHubURL(hubURL)
	return &HubWorkspaceStreamer{
		HubURL:     hubURL,
		Delegation: delegation,
		HTTPClient: httpClient,
		ConnectURL: connectURL,
	}
}

// StreamHub satisfies HubStreamer. `WatchOrg` is the only supported
// method: spawned-agent CLI invocations consume the org-scoped CRDT
// stream by tunneling `/ws/orgevents` through the same delegation-
// token channel as unary calls.
func (s *HubWorkspaceStreamer) StreamHub(ctx context.Context, userID, method string, payload []byte, onPayload func([]byte) error) error {
	if s.Delegation == nil {
		return errors.New("remoteipc: delegation store not configured")
	}
	switch method {
	case "WatchOrg":
		return s.watchOrg(ctx, userID, payload, onPayload)
	default:
		return fmt.Errorf("remoteipc: hub stream method not implemented: %s", method)
	}
}

// watchOrg opens `/ws/orgevents` upstream with a fresh delegation
// bearer and forwards each binary WS frame as the protobuf payload
// of a marshalled WatchOrgEvent. The framing format on /ws/orgevents
// is `[4-byte big-endian length][protobuf WatchOrgEvent]`; we strip
// the length prefix before re-emitting so the IPC consumer sees
// proto bytes directly.
func (s *HubWorkspaceStreamer) watchOrg(ctx context.Context, userID string, payload []byte, onPayload func([]byte) error) error {
	var req leapmuxv1.WatchOrgRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("decode WatchOrgRequest: %w", err)
	}
	scopeWorkspace := ""
	if len(req.GetWorkspaceIds()) > 0 {
		scopeWorkspace = req.GetWorkspaceIds()[0]
	}
	bearer, err := s.Delegation.GetBearer(ctx, crossworker.DelegationScope{UserID: userID, WorkspaceID: scopeWorkspace})
	if err != nil {
		return fmt.Errorf("delegation bearer: %w", err)
	}

	ws, err := channelwire.OpenOrgEventsWS(ctx, s.HTTPClient, s.ConnectURL, bearer, req.GetOrgId(), req.GetWorkspaceIds())
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
	// Strip the 4-byte length prefix so the IPC consumer receives the
	// WatchOrgEvent proto bytes directly.
	return channelwire.RunOrgEventsReadLoop(ctx, ws, true, onPayload)
}
