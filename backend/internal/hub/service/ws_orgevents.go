package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// OrgEventsHandler upgrades to a WebSocket and streams `WatchOrgEvent`
// frames for the requested (org_id, workspace_ids) subscription. This
// is the sole transport for org-event subscriptions; the OrgCRDT
// ConnectRPC service exposes only unary calls (SubmitOps,
// UpdatePresence).
//
// Why WebSocket rather than a ConnectRPC server-stream? ConnectRPC
// streaming over HTTP/1.1 can be silently buffered by some proxies +
// the desktop sidecar's Tauri proxy; WebSocket negotiates an Upgrade
// and bypasses those layers. The previous `OrgCRDT.WatchOrg` streaming
// RPC was retired in favor of this endpoint.
//
// Wire format: each frame is a length-prefixed protobuf-marshaled
// `WatchOrgEvent` (4-byte big-endian uint32 length + payload). Mirrors
// the channelwire framing used by `/ws/channel` so consumers can share
// a single read helper.
//
// Subprotocol: `orgevents-relay`. The initial subscription parameters
// come from the URL query (`?org_id=...&workspace_ids=ws1,ws2`). This
// keeps the connection's subscription stable for its entire lifetime
// — to change the workspace filter, the client reopens the WS.
type OrgEventsHandler struct {
	store          store.Store
	registry       *crdt.Registry
	soloUser       *auth.UserInfo
	secureCookie   bool
	tokenValidator *auth.TokenValidator
}

// NewOrgEventsHandler returns a handler ready to mount at
// `/ws/orgevents`. The token validator is optional; when unset, the
// handler accepts cookie auth only.
func NewOrgEventsHandler(
	st store.Store,
	registry *crdt.Registry,
	soloUser *auth.UserInfo,
	secureCookie bool,
) *OrgEventsHandler {
	return &OrgEventsHandler{
		store:        st,
		registry:     registry,
		soloUser:     soloUser,
		secureCookie: secureCookie,
	}
}

// WithTokenValidator wires Bearer-auth support. Returns the receiver
// for chaining.
func (h *OrgEventsHandler) WithTokenValidator(v *auth.TokenValidator) *OrgEventsHandler {
	h.tokenValidator = v
	return h
}

func (h *OrgEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := h.authenticate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		http.Error(w, "missing org_id", http.StatusBadRequest)
		return
	}
	workspaceIDs := []string{}
	if raw := r.URL.Query().Get("workspace_ids"); raw != "" {
		for _, w := range strings.Split(raw, ",") {
			if w = strings.TrimSpace(w); w != "" {
				workspaceIDs = append(workspaceIDs, w)
			}
		}
	}

	mgr, err := h.registry.Get(r.Context(), orgID)
	if err != nil {
		http.Error(w, "registry get: "+err.Error(), http.StatusInternalServerError)
		return
	}

	allowed, err := resolveAllowedWorkspacesSet(r.Context(), h.store, orgID, workspaceIDs, user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"orgevents-relay"},
	})
	if err != nil {
		slog.Error("orgevents websocket upgrade failed", "user_id", user.ID, "error", err)
		return
	}
	// Per-message read limit so a malformed client can't blow our
	// memory. WatchOrgEvent payloads are bounded by the protobuf
	// envelope; 16 MiB is the same ceiling channelwire uses.
	wsConn.SetReadLimit(16 * 1024 * 1024)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 64-deep buffer covers the bootstrap-burst window where a fresh
	// subscriber sees every entity_materialized at once. The Send below
	// is non-blocking: a full buffer triggers ErrSubscriberSlow which
	// cancels this subscriber's ctx and tears down the connection. Per
	// the Subscriber.Send contract the manager must not be blocked by
	// one slow client — back-pressure here would head-of-line stall
	// every other subscriber in the org.
	pending := make(chan *crdt.MarshaledEvent, 64)
	sub := &crdt.Subscriber{
		UserID:   user.ID,
		ClientID: presenceClientID(user),
		Filter:   crdt.SubscriberFilter{WorkspaceIDs: allowed},
		Send: func(evt *crdt.MarshaledEvent) error {
			select {
			case pending <- evt:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			default:
				// Buffer is full: drop this subscriber rather than block
				// the manager goroutine. Cancelling ctx causes the writer
				// loop below to exit and the deferred unsub to fire,
				// removing us from the manager's subs set so subsequent
				// broadcasts skip us entirely.
				slog.Warn("orgevents: subscriber buffer full, dropping connection",
					"user_id", user.ID, "client_id", presenceClientID(user))
				cancel()
				return crdt.ErrSubscriberSlow
			}
		},
	}
	initial, unsub := mgr.Subscribe(sub)
	defer unsub()

	// Stamp the hub-derived client identity so the frontend's
	// active-client gate has something stable to compare against. The
	// derivation (session id → bearer token id → user id) is shared
	// with `UpdatePresence` and the manager's presence refcount, so
	// both the local gate's comparison value and the server's
	// disconnect-driven cleanup pivot on the same id.
	initial.SubscriberClientId = sub.ClientID

	defer func() {
		_ = wsConn.Close(websocket.StatusNormalClosure, "")
	}()

	// Send the initial OrgMaterialized as the first frame. The initial
	// payload is per-subscriber-unique (their filter dictates the
	// materialized rows) so wrap in a fresh MarshaledEvent.
	initialEvt := crdt.NewMarshaledEvent(&leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Initial{Initial: initial},
	})
	if err := writeOrgEvent(ctx, wsConn, initialEvt); err != nil {
		slog.Debug("orgevents: write initial failed", "user_id", user.ID, "error", err)
		return
	}

	// Drain client-side reads in a goroutine so the WebSocket library
	// observes the close frame promptly when the peer disconnects.
	// Clients don't send subscription updates after the initial URL
	// query, so we discard whatever they send.
	go func() {
		defer cancel()
		for {
			_, _, err := wsConn.Read(ctx)
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-pending:
			if err := writeOrgEvent(ctx, wsConn, evt); err != nil {
				slog.Debug("orgevents: write event failed", "user_id", user.ID, "error", err)
				return
			}
		}
	}
}

// writeOrgEvent serializes evt into a length-prefixed binary frame on
// the WS via channelwire.WriteFramedBytes (the same framing
// /ws/channel uses, so a single client-side read helper handles
// both relays).
//
// The MarshaledEvent's lazy Bytes() cache means broadcasts shared
// across N subscribers pay the proto.Marshal cost ONCE: the first WS
// writer that reaches this function fills the cache and N−1 others
// reuse the result.
func writeOrgEvent(ctx context.Context, ws *websocket.Conn, evt *crdt.MarshaledEvent) error {
	data, err := evt.Bytes()
	if err != nil {
		return fmt.Errorf("marshal orgevent: %w", err)
	}
	return channelwire.WriteFramedBytes(ctx, ws, data)
}

// authenticate resolves the caller via the shared HTTP auth ladder
// (solo → bearer → cookie) so this endpoint stays interchangeable with
// /ws/channel for credential plumbing.
func (h *OrgEventsHandler) authenticate(r *http.Request) (*auth.UserInfo, error) {
	return auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:     h.store,
		Validator: h.tokenValidator,
		SoloUser:  h.soloUser,
		Cookies:   []bool{h.secureCookie},
	})
}
