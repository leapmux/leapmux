package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
)

// UserEventsHandler upgrades to a WebSocket and streams `WatchUserEvent`
// frames for the authenticated user's (workspace_ids) subscription. This
// is the sole transport for user-event subscriptions; the UserCRDT
// ConnectRPC service exposes only unary calls (SubmitOps,
// UpdatePresence).
//
// Why WebSocket rather than a ConnectRPC server-stream? ConnectRPC
// streaming over HTTP/1.1 can be silently buffered by some proxies +
// the desktop sidecar's Tauri proxy; WebSocket negotiates an Upgrade
// and bypasses those layers. The previous streaming Watch RPC was
// retired in favor of this endpoint.
//
// Wire format: each frame is a length-prefixed protobuf-marshaled
// `WatchUserEvent` (4-byte big-endian uint32 length + payload). Mirrors
// the channelwire framing used by `/ws/channel` so consumers can share
// a single read helper.
//
// Subprotocol: `userevents-relay`. The initial subscription parameters
// come from the URL query (`?workspace_ids=ws1,ws2`); tenancy is the
// authenticated user. This keeps the connection's subscription stable
// for its entire lifetime — to change the workspace filter, the client
// reopens the WS.
type UserEventsHandler struct {
	wsAuthenticator
	registry *crdt.Registry
}

// NewUserEventsHandler returns a handler ready to mount at
// `/ws/userevents`. The token validator is optional; when unset, the
// handler accepts cookie auth only.
func NewUserEventsHandler(
	st store.Store,
	registry *crdt.Registry,
	authContexts *auth.AuthContextRegistry,
	soloUser *auth.UserInfo,
	secureCookie bool,
) *UserEventsHandler {
	return &UserEventsHandler{
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
func (h *UserEventsHandler) WithTokenValidator(v *auth.TokenValidator) *UserEventsHandler {
	h.tokenValidator = v
	return h
}

// countConnectWithoutBootstrap counts an authenticated connect that failed
// before SubscribeWithACL could select a bootstrap arm.
//
// The labels are read off a ZERO SubscribeOutcome rather than written as the
// string literals "invalid", "invalid": those values are Prometheus label
// values, so the vocabulary needs exactly one producer -- the enums' own
// String()/Label(), which a dashboard-breaking rename would have to go through.
func countConnectWithoutBootstrap() {
	var none crdt.SubscribeOutcome
	metrics.UserEventsSubscribeTotal.WithLabelValues(none.Mode().Label(), none.Reason().Label()).Inc()
}

// newUserEventsSubscriber builds this connection's crdt.Subscriber: its
// identity, its transport queue, and the two-phase OVERFLOW POLICY.
//
// The policy is the reason this is a function. It is a real decision -- which of
// two very different answers a dropped frame gets, depending on whether the
// bootstrap frame is on the wire yet -- and inline it was an anonymous closure
// inside a struct literal inside a 280-line HTTP handler, reachable only through
// httptest.NewServer plus a real websocket.Dial. That is the same untestability
// subscriberQueue's own doc gives as the reason IT was extracted, one layer up.
//
// `cancel` is the connection's, not the request's: the steady-state arm uses it
// to tear the connection down, which exits the writer loop and fires the
// deferred unsub.
func newUserEventsSubscriber(
	ctx context.Context,
	cancel context.CancelFunc,
	user *auth.UserInfo,
	clientID string,
	requested map[string]bool,
) (*crdt.Subscriber, *subscriberQueue) {
	// 64-deep buffer covers the steady-state burst window after bootstrap.
	// During SubscribeWithACL's RESUME path the subscriber is registered
	// before the writer loop starts, and live broadcasts enqueue via Send
	// while the journal scan runs — parking those frames in a slice (instead
	// of the bounded channel) so a multi-page scan under load cannot trip
	// ErrSubscriberSlow and drop the reconnect. The two phases, the cap, the
	// mutex and the buffers all live in subscriberQueue -- see its doc for why
	// they are one value rather than five locals mutated from three places.
	queue := newSubscriberQueue()
	sub := &crdt.Subscriber{
		UserID:                user.ID.String(),
		ClientID:              clientID,
		RequestedWorkspaceIDs: requested,
		// Filter is resolved and installed under subscribeExpandMu by
		// SubscribeWithACL below (see the resolve-then-register TOCTOU it closes).
		Send: func(evt *crdt.MarshaledEvent) error {
			err := queue.Send(ctx, evt)
			if !errors.Is(err, crdt.ErrSubscriberSlow) {
				return err
			}
			// The two phases overflow for different reasons and deserve
			// different answers.
			if !queue.Released() {
				// PRE-BOOTSTRAP: the bootstrap frame is still being built --
				// either a resume's journal scan or a fallback's baseline walk,
				// both of which now run with this subscriber registered and no
				// manager lock held. Tearing down here sent the client back with
				// the same cursor to rebuild the same work, under the load that
				// caused the overflow. Flag it instead: the manager consults
				// Overflowed() after each, and rebaselines (which discards the
				// parked buffer AND clears this flag) before taking a fresh
				// baseline -- so the frame this call could not hold is
				// superseded rather than lost.
				slog.Warn("userevents: park buffer full while building the bootstrap frame, falling back to a snapshot",
					"user_id", user.ID, "client_id", clientID)
				return err
			}
			// STEADY STATE: there is no scan to fall back from, so drop the
			// subscriber rather than block the manager goroutine. Cancelling ctx
			// exits the writer loop below and fires the deferred unsub, so later
			// broadcasts skip us.
			slog.Warn("userevents: subscriber buffer full, dropping connection",
				"user_id", user.ID, "client_id", clientID)
			cancel()
			return err
		},
		// A resume that gives up, or a fallback whose park buffer overflowed,
		// re-registers and takes a baseline at a LATER point than anything
		// parked before it. Those parked frames are superseded by the baseline,
		// and replaying them over it would reinstate stale entity records for
		// good (the client applies materialized / removed wholesale, with no HLC
		// compare). The manager calls this inside the same projection hold that
		// re-registers and captures the generation (registerForFallback), so
		// dropping the buffer here discards precisely the superseded frames and
		// nothing newer.
		OnRebaseline: queue.Rebaseline,
		Overflowed:   queue.Overflowed,
	}
	return sub, queue
}

func (h *UserEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := h.authenticate(r)
	if err != nil {
		writeHTTPAuthError(w, "user events", err)
		return
	}

	// Tenancy is the authenticated user. No client-supplied user_id query
	// parameter: channelwire.UserEventsURL encodes only workspace_ids and the
	// resume cursor (resume_after_hlc / resume_epoch), and accepting a foreign
	// id would let any authenticated user drive registry.Get (which performs no
	// authorization) into bootstrapping an arbitrary tenant's CRDT Manager.
	userID := user.ID.String()
	workspaceIDs := []string{}
	if raw := r.URL.Query().Get("workspace_ids"); raw != "" {
		for _, w := range strings.Split(raw, ",") {
			if w = strings.TrimSpace(w); w != "" {
				workspaceIDs = append(workspaceIDs, w)
			}
		}
	}
	// Resume cursor: a reconnecting client presents its last-applied HLC plus
	// the epoch it saw it under. Both params absent is a first connect (the
	// hub's SubscribeWithACL treats a nil cursor as FALLBACK → full snapshot).
	// A malformed resume_after_hlc or resume_epoch is a genuine client bug, not
	// a legacy client, so it is rejected with 400 rather than silently degrading
	// (silent degradation would mask the bug and let a broken client limp along
	// with full-snapshot reconnects forever). resume_epoch without a matching
	// resume_after_hlc is likewise malformed.
	resumeCursor, resumeEpoch, resumeErr := channelwire.ParseResumeCursorFromQuery(r.URL.Query())
	if resumeErr != nil {
		countConnectWithoutBootstrap()
		http.Error(w, resumeErr.Error(), http.StatusBadRequest)
		return
	}
	// A NARROWED subscription may not present a cursor, and the hub is where
	// that is enforced -- not the client.
	//
	// The persisted cursor is per-USER, not per-filter, so one minted under a
	// narrow filter and replayed under a wider one can miss ops (see
	// SubscribeWithACL's max_hlc paragraph). Since the checkpoint seed landed,
	// cursors are cross-TAB as well, so two tabs need not even agree on what the
	// filter was. The browser already refuses the pairing, but leaving it there
	// makes a correctness invariant the property of one client out of three: a
	// future desktop bridge or a CLI that persists a cursor would violate it
	// silently and lose ops with nothing server-side to say so.
	//
	// REJECTED rather than silently degraded to a snapshot, for the reason the
	// malformed-cursor arm above gives: degradation masks the bug and lets a
	// broken client limp along forever. Nothing in tree sends this pairing --
	// remoteipc's hub_stream and the remote CLI both pass their workspace ids
	// with a nil cursor -- so this narrows the contract without breaking a
	// caller. Permitting a SAFE narrowed resume needs the mint-time filter
	// carried alongside the cursor and re-checked here, which this deliberately
	// leaves open rather than pre-empting.
	if len(workspaceIDs) > 0 && resumeCursor != nil {
		countConnectWithoutBootstrap()
		http.Error(w, "resume_after_hlc is not accepted with workspace_ids: a cursor minted under a "+
			"narrower filter cannot be replayed under a wider one", http.StatusBadRequest)
		return
	}

	mgr, err := h.registry.Get(r.Context(), userID)
	if err != nil {
		countConnectWithoutBootstrap()
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
		Subprotocols: []string{"userevents-relay"},
	})
	if err != nil {
		countConnectWithoutBootstrap()
		slog.Error("userevents websocket upgrade failed", "user_id", user.ID, "error", err)
		return
	}
	// Per-message read limit so a malformed client can't blow our
	// memory. WatchUserEvent payloads are bounded by the protobuf
	// envelope. Named rather than repeated as a literal so this socket
	// and the subscribers reading from it cannot drift apart: the two
	// matching was previously only a claim in this comment.
	wsConn.SetReadLimit(channelwire.UserEventsReadLimit)

	ctx, cleanupLease, current := h.authLease.bind(r.Context(), user, wsConn)
	if !current {
		countConnectWithoutBootstrap()
		return
	}
	defer cleanupLease()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 64-deep buffer covers the steady-state burst window after bootstrap.
	// During SubscribeWithACL's RESUME path the subscriber is registered
	// before this writer loop starts, and live broadcasts enqueue via Send
	// while the journal scan runs — parking those frames in a slice (instead
	// of the bounded channel) so a multi-page scan under load cannot trip
	// ErrSubscriberSlow and drop the reconnect. The two phases, the cap, the
	// mutex and the buffers all live in subscriberQueue -- see its doc for why
	// they are one value rather than five locals mutated from three places here.
	// Derived ONCE, and used both to register and to log. Recomputing it per log
	// site let a line name an identity the registered subscriber does not have
	// if the derivation ever grew a condition.
	clientID := presenceClientID(user)
	sub, queue := newUserEventsSubscriber(ctx, cancel, user, clientID, requested)
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
	// Resolve the workspace filter, register the subscriber, and emit the
	// bootstrap frame. SubscribeWithACL returns a discriminated SubscribeOutcome:
	// Mode == ResumeDelta (the post-cursor op tail) when the client's cursor is
	// still within the compaction window, or Mode == SubscribeInitial (a full
	// UserMaterialized snapshot) otherwise — including a first-connect with no
	// cursor. Exactly one bootstrap arm is selected; the first frame on the wire
	// is therefore exactly one of `delta` or `initial`, never both. The same
	// resolve callback and subscribeExpandMu serialization apply to both paths.
	resolve := func() (map[string]bool, error) {
		return resolveAllowedWorkspacesSetForUser(ctx, h.store, workspaceIDs, user)
	}
	subscribeStart := time.Now()
	out, err := mgr.SubscribeWithACL(ctx, sub, resumeCursor, resumeEpoch, resolve)
	elapsed := time.Since(subscribeStart)
	// Labelled here rather than inside crdt: that package has no
	// internal/metrics edge, and Reason() exists precisely so the service layer
	// can label without re-deriving the verdict.
	//
	// Counted on BOTH arms so the series is a complete partition of connect
	// outcomes. Counting only successes made the metric drop silently exactly
	// when it mattered -- a deployment whose ACL resolve started erroring showed
	// a fall in subscribe volume indistinguishable from a fall in traffic, with
	// no error series to explain it. A failed connect selected no bootstrap arm,
	// so it is labelled with the zero mode/reason, both of which already spell
	// themselves "invalid".
	//
	// "Complete" means every AUTHENTICATED connect: the earlier returns above --
	// a malformed cursor, a registry bootstrap failure, a refused upgrade, a
	// superseded auth lease -- count too, through countConnectWithoutBootstrap.
	// Without them the very failure this comment names was still invisible:
	// registry.Get is where the manager loads its state from the journal, so a
	// hub whose user_state read starts failing returns before this line for
	// every connect, and the series falls to zero with nothing to explain it.
	mode := out.Mode().Label()
	metrics.UserEventsSubscribeTotal.WithLabelValues(mode, out.Reason().Label()).Inc()
	metrics.UserEventsSubscribeDuration.WithLabelValues(mode).Observe(elapsed.Seconds())
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			_ = wsConn.Close(websocket.StatusPolicyViolation, "forbidden")
		} else {
			slog.Error("userevents: resume setup failed", "user_id", user.ID, "error", err)
			_ = wsConn.Close(websocket.StatusTryAgainLater, "temporarily unavailable, retry")
		}
		return
	}
	slog.Debug("userevents: subscribed",
		"user_id", user.ID,
		"client_id", clientID,
		"mode", out.Mode(),
		"reason", out.Reason(),
		// MICROseconds, for the reason the histogram beside it has hand-picked
		// buckets: a resume is microseconds against a fallback baseline's
		// milliseconds, so an int64 millisecond truncation reported every single
		// resume as 0 -- and this line is the only place the number is
		// attributable to a user_id and client_id.
		"duration_us", elapsed.Microseconds())
	defer out.Unsub()()

	defer func() {
		_ = wsConn.Close(websocket.StatusNormalClosure, "")
	}()

	// Send the first (and only bootstrap) frame. BOTH arms stamp
	// subscriber_client_id: it is the frontend active-client gate's only source
	// of its own identity, and since the client-checkpoint work a resume is the
	// normal COLD-START path — a refreshed page hydrates from IndexedDB and
	// resumes, so there is no earlier `initial` frame it could have learned the
	// id from. (It used to be delta-only-safe on the assumption that a resume
	// always followed a bootstrap in the same page session; cross-refresh resume
	// broke that assumption.)
	//
	// The hub-derived client identity it stamps is the same namespaced
	// derivation (session id → bearer kind/token id → user id) that
	// `UpdatePresence` and the manager's presence refcount use, so the local
	// gate's comparison value and the server's disconnect-driven cleanup pivot
	// on one id. userID comes from the authenticated session — the same value
	// the manager was resolved under.
	//
	// Either payload is per-subscriber-unique (their filter dictates the rows),
	// so it wraps in a fresh MarshaledEvent rather than a shared one.
	bootstrapEvt := crdt.NewMarshaledEvent(out.Bootstrap(sub.ClientID, userID))
	if err := writeUserEvent(ctx, wsConn, bootstrapEvt); err != nil {
		slog.Debug("userevents: write bootstrap failed", "user_id", user.ID, "mode", out.Mode(), "error", err)
		return
	}

	// Open the bounded live queue and flush frames that parked during the
	// subscribe/scan window (before this writer loop). Order: bootstrap
	// frame (above) → parked live catch-up → steady-state live queue.
	parked, dropped := queue.Release()
	if dropped {
		// The park buffer overflowed after the manager's last Overflowed()
		// check -- i.e. during the bootstrap WRITE, which on a large account is
		// the longest stretch of the parking window. Flushing what survived
		// would leave this connection permanently short of whatever was
		// dropped, silently and for its whole session, because liveCatchUpSink
		// discards the send error and nothing downstream re-checks.
		//
		// Dropping the connection is the recovery, not just the alarm: every
		// parked BATCH frame is strictly above the bootstrap's max_hlc (that is
		// what sendTo's resumeSuppressThrough gate guarantees), so the client
		// reconnects with that cursor and delta-resumes exactly the span this
		// connection could not deliver. The deferred Close below fires on
		// return, the same teardown the steady-state overflow arm already uses.
		//
		// The buffer also holds PRESENCE and WORKSPACE-LIFECYCLE frames, which
		// broadcastTo sends outside that gate and which no ResumeDelta replays --
		// so the delta argument above does not cover them. The reconnect does:
		// the client re-derives workspace membership from the bootstrap frame
		// (adoptBootstrapFrame fires onWorkspaceLifecycleChanged on EVERY
		// bootstrap, for exactly this reason) and presence from the heartbeat it
		// pings on the bootstrapped edge. Excluding those frames from parking
		// instead would trade a bounded drop-and-rebootstrap for a silent
		// per-frame loss, which is strictly worse.
		slog.Warn("userevents: park buffer overflowed while writing the bootstrap frame; dropping the connection to force a re-bootstrap",
			"user_id", user.ID, "client_id", clientID)
		return
	}
	for _, evt := range parked {
		if err := writeUserEvent(ctx, wsConn, evt); err != nil {
			slog.Debug("userevents: write parked event failed", "user_id", user.ID, "error", err)
			return
		}
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
		case evt := <-queue.Live():
			if err := writeUserEvent(ctx, wsConn, evt); err != nil {
				slog.Debug("userevents: write event failed", "user_id", user.ID, "error", err)
				return
			}
		}
	}
}

// writeUserEvent serializes evt into a length-prefixed binary frame on
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
// already feeds it through subscriberQueue's bounded live channel, whose Send drops
// and cancels rather than blocking, so the backlog is bounded at the
// source and a relayWriter here would only queue behind a queue.
//
// What was genuinely missing is a bound on ONE write. Without it a
// client that accepts the connection and stops reading parks this
// goroutine inside the write forever, pinning the subscription and the
// conn -- and the broadcaster's own escape hatch does not help, because
// it only fires once enough further events pile up, which on a quiet
// tenant never happens. Cancelling the write context tears the connection
// down, which is the intended recovery: the client reconnects and re-reads
// the materialized state.
func writeUserEvent(ctx context.Context, ws *websocket.Conn, evt *crdt.MarshaledEvent) error {
	data, err := evt.Bytes()
	if err != nil {
		return fmt.Errorf("marshal userevent: %w", err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
	defer cancel()
	return channelwire.WriteFramedBytes(writeCtx, ws, data)
}
