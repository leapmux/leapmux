package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
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
	wsAuthenticator
	registry *crdt.Registry
}

// NewOrgEventsHandler returns a handler ready to mount at
// `/ws/orgevents`. The token validator is optional; when unset, the
// handler accepts cookie auth only.
func NewOrgEventsHandler(
	st store.Store,
	registry *crdt.Registry,
	authContexts *auth.AuthContextRegistry,
	soloUser *auth.UserInfo,
	secureCookie bool,
) *OrgEventsHandler {
	return &OrgEventsHandler{
		wsAuthenticator: wsAuthenticator{
			store:        st,
			authLease:    newWebSocketAuthLease(authContexts),
			soloUser:     soloUser,
			secureCookie: secureCookie,
		},
		registry: registry,
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
		writeHTTPAuthError(w, "organization events", err)
		return
	}

	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		http.Error(w, "missing org_id", http.StatusBadRequest)
		return
	}
	// Refuse a foreign org id before touching the registry. Without this the
	// caller-supplied org_id would let any authenticated user drive
	// registry.Get (which performs no authorization) into bootstrapping an
	// arbitrary tenant's CRDT Manager and then pin it janitor-immune by
	// registering a live subscriber -- a resource-materialization vector, even
	// though the empty ACL filter suppresses every workspace-scoped payload.
	// ResolveOrgID is the same pure "foreign org -> NotFound" comparison the
	// ConnectRPC workspace path uses; org_id is non-empty here, so it either
	// equals the caller's personal org or fails closed.
	if _, err := auth.ResolveOrgID(user, orgID); err != nil {
		http.Error(w, "organization not found", http.StatusNotFound)
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

	var requested map[string]bool
	if len(workspaceIDs) > 0 {
		requested = make(map[string]bool, len(workspaceIDs))
		for _, workspaceID := range workspaceIDs {
			requested[workspaceID] = true
		}
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
	// envelope. Named rather than repeated as a literal so this socket
	// and the subscribers reading from it cannot drift apart: the two
	// matching was previously only a claim in this comment.
	wsConn.SetReadLimit(channelwire.OrgEventsReadLimit)

	ctx, cleanupLease, current := h.authLease.bind(r.Context(), user, wsConn)
	if !current {
		return
	}
	defer cleanupLease()
	ctx, cancel := context.WithCancel(ctx)
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
		UserID:                user.ID.String(),
		ClientID:              presenceClientID(user),
		RequestedWorkspaceIDs: requested,
		WorkspaceScopeID:      user.Credential.WorkspaceScopeID(),
		// Filter is resolved and installed under subscribeExpandMu by
		// SubscribeWithACL below (see the resolve-then-register TOCTOU it closes).
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
	// Resolve the workspace filter and register the subscriber atomically under
	// the manager's subscribe/expand lock. This closes the resolve-then-register
	// window: a workspace create that commits while this connection is being set
	// up either is seen by the resolve (workspace already included) or catches
	// this now-registered subscriber in its expand pass -- it can no longer leave
	// the subscriber holding a stale filter that misses the new workspace until
	// reconnect. The resolve reads the DB ACL under that lock; its failure closes
	// the just-accepted socket. Only a delegation-scope PermissionDenied is a genuine
	// authorization failure (policy violation); anything else -- a transient store
	// error -- closes with TryAgainLater so the client classifies it as a
	// recoverable close and reconnects (see channelwire.isRecoverableCloseCode;
	// StatusInternalError is terminal there and would surface as a fatal stream
	// error instead). Keying on the specific authz code keeps this robust if the
	// callee's error coding changes.
	initial, unsub, err := mgr.SubscribeWithACL(sub, func() (map[string]bool, error) {
		return resolveAllowedWorkspacesSetForUser(ctx, h.store, auth.BindOrg(orgID), workspaceIDs, user)
	})
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			_ = wsConn.Close(websocket.StatusPolicyViolation, "forbidden")
		} else {
			slog.Error("orgevents: resolve allowed workspaces failed", "user_id", user.ID, "error", err)
			_ = wsConn.Close(websocket.StatusTryAgainLater, "temporarily unavailable, retry")
		}
		return
	}
	defer unsub()

	// Stamp the hub-derived client identity so the frontend's
	// active-client gate has something stable to compare against. The
	// namespaced derivation (session id → bearer kind/token id → user
	// id) is shared
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
// Each write is bounded by relayWriteTimeout, the same per-write budget
// the channel relay applies.
//
// This socket does NOT need the channel relay's queue: the broadcaster
// already feeds it through a bounded `pending` channel whose Send drops
// and cancels rather than blocking, so the backlog is bounded at the
// source and a relayWriter here would only queue behind a queue.
//
// What was genuinely missing is a bound on ONE write. Without it a
// client that accepts the connection and stops reading parks this
// goroutine inside the write forever, pinning the subscription and the
// conn -- and the broadcaster's own escape hatch does not help, because
// it only fires once enough further events pile up, which on a quiet org
// never happens. Cancelling the write context tears the connection down,
// which is the intended recovery: the client reconnects and re-reads the
// materialized state.
func writeOrgEvent(ctx context.Context, ws *websocket.Conn, evt *crdt.MarshaledEvent) error {
	data, err := evt.Bytes()
	if err != nil {
		return fmt.Errorf("marshal orgevent: %w", err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
	defer cancel()
	return channelwire.WriteFramedBytes(writeCtx, ws, data)
}
