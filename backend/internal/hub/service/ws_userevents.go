package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
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
	// queuePool is the shared byte budget every subscriber's queue draws on, so
	// the Hub's user-event backlog is bounded across connections rather than
	// only within each one.
	queuePool *sendq.Pool
	// bootstrapGate bounds how many opening frames may be under CONSTRUCTION at
	// once, which is the one term queuePool cannot bound.
	//
	// The pool charges a bootstrap frame only once it exists -- a size is the
	// first thing it can ask about -- so the build itself is unbudgeted, and the
	// refusal it hands a client that arrives too late is StatusTryAgainLater,
	// which sends that client back to build the same full-account snapshot
	// again. Under a reconnect storm, which is the scenario the whole budget
	// exists for, the pool's own backpressure became the driver of allocations
	// it could not see: used_bytes sat under Capacity while RSS climbed.
	//
	// Buffered to the number of worst-case snapshots the budget could hold
	// anyway (see NewUserEventsHandler), so the gate and the charge state one
	// number and raising userevents_queue_memory_budget buys builders too.
	bootstrapGate chan struct{}
}

// NewUserEventsHandler returns a handler ready to mount at
// `/ws/userevents`. The token validator is optional; when unset, the
// handler accepts cookie auth only.
//
// queuePool is required: a composition that forgot it would leave this path the
// one outbound queue in the Hub with no memory bound, which is exactly the
// state this argument exists to end. Panicking here makes that a startup
// failure rather than a slow leak nobody attributes.
func NewUserEventsHandler(
	st store.Store,
	registry *crdt.Registry,
	authContexts *auth.AuthContextRegistry,
	soloUser *auth.UserInfo,
	secureCookie bool,
	queuePool *sendq.Pool,
) *UserEventsHandler {
	if queuePool == nil {
		panic("service: NewUserEventsHandler requires a queue pool")
	}
	// One worst-case opening frame is a whole account's visible state at the
	// socket's read limit, charged at crdt.ResidentFactor because the queue
	// holds the proto tree beside the buffer. Capacity divided by that is how
	// many the budget could hold at once -- so a build admitted here is one the
	// pool could still charge for when it finishes, and the gate never refuses
	// a connect the budget would have served. At least one, because a pool
	// smaller than a single frame must still let a connect try and be told
	// terminally that it can never fit.
	concurrentBuilds := max(1, queuePool.Capacity()/
		(int64(channelwire.UserEventsReadLimit)*crdt.ResidentFactor))
	return &UserEventsHandler{
		wsAuthenticator: newWSAuthenticator(st, authContexts, soloUser, secureCookie),
		registry:        registry,
		queuePool:       queuePool,
		bootstrapGate:   make(chan struct{}, concurrentBuilds),
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
// identity, its transport queue, and the wiring that hands a refused frame to
// the two-phase OVERFLOW POLICY (answerRefusal).
//
// That policy is the reason this is a function. It is a real decision -- which
// of two very different answers a dropped frame gets -- and inline it was an
// anonymous closure inside a struct literal inside a 280-line HTTP handler,
// reachable only through httptest.NewServer plus a real websocket.Dial. That is
// the same untestability subscriberQueue's own doc gives as the reason IT was
// extracted, one layer up.
//
// `cancel` is the connection's, not the request's: the steady-state arm of the
// policy uses it to tear the connection down, which exits the writer loop and
// fires the deferred unsub. The pool's evict hook below uses the same one.
func newUserEventsSubscriber(
	ctx context.Context,
	cancel context.CancelFunc,
	user *auth.UserInfo,
	clientID string,
	requested map[string]bool,
	queuePool *sendq.Pool,
) (*crdt.Subscriber, *subscriberQueue) {
	// 64-deep buffer covers the steady-state burst window after bootstrap.
	// During SubscribeWithACL's RESUME path the subscriber is registered
	// before the writer loop starts, and live broadcasts enqueue via Send
	// while the journal scan runs — parking those frames in a slice (instead
	// of the bounded channel) so a multi-page scan under load cannot trip
	// ErrSubscriberSlow and drop the reconnect. The two phases, the cap, the
	// byte budget, the mutex and the buffers all live in subscriberQueue -- see
	// its doc for why they are one value rather than five locals mutated from
	// three places.
	//
	// The evict hook is the same teardown the steady-state overflow arm below
	// uses, for the same reason: cancelling the connection's context exits the
	// writer loop and fires the deferred unsub.
	var evictOnce sync.Once
	queue := newSubscriberQueue(queuePool, func(err error) bool {
		// Reports whether this call started the teardown. A second nomination of
		// a subscriber already unwinding reclaims nothing, and counting it would
		// overstate "connections disconnected to reclaim memory".
		started := false
		evictOnce.Do(func() {
			started = true
			slog.Warn("userevents: subscriber reclaimed to free shared queue memory",
				"user_id", user.ID, "client_id", clientID, "error", err)
			metrics.CountSendqGiveUp(metrics.PoolUserEvents, sendq.GiveUpPoolPressure.Label())
			cancel()
		})
		return started
	})
	sub := &crdt.Subscriber{
		UserID:                user.ID.String(),
		ClientID:              clientID,
		RequestedWorkspaceIDs: requested,
		// Filter is resolved and installed under subscribeExpandMu by
		// SubscribeWithACL below (see the resolve-then-register TOCTOU it closes).
		Send: func(evt *crdt.MarshaledEvent) error {
			err := queue.Send(ctx, evt)
			var refusal queueRefusal
			if !errors.As(err, &refusal) {
				return err
			}
			answerRefusal(refusal, cancel, user, clientID)
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

// answerRefusal is the two-phase OVERFLOW POLICY: which of two very different
// answers a dropped frame gets.
//
// It routes on the REFUSAL, which carries the phase and the bound that actually
// fired, captured under the queue's own lock in the statement that counted the
// drop. It used to ask the queue instead -- a phase query for one, a sentinel
// match for the other -- and neither answer was reliable by the time it asked.
// The Send closure runs on the crdt broadcast goroutine while the
// writer loop flips the phase from its own, across the whole bootstrap WRITE
// (see subscriberQueue.Bootstrapped), so a PARKING refusal could arrive here
// after the flip and be answered down the steady-state arm: a slow-client log
// line for a full shared budget, and a cancel for a connection that was falling
// back perfectly well.
//
// `cancel` is the connection's, not the request's: the steady-state arm uses it
// to tear the connection down, which exits the writer loop and fires the
// deferred unsub.
func answerRefusal(refusal queueRefusal, cancel context.CancelFunc, user *auth.UserInfo, clientID string) {
	if refusal.phase == dropPhaseParking {
		// PRE-BOOTSTRAP: the bootstrap frame is still being built -- either a
		// resume's journal scan or a fallback's baseline walk, both of which run
		// with this subscriber registered and no manager lock held. Tearing down
		// here sent the client back with the same cursor to rebuild the same
		// work, under the load that caused the overflow. Flag it instead: the
		// manager consults Overflowed() after each, and rebaselines (which
		// discards the parked buffer AND clears this flag) before taking a fresh
		// baseline -- so the frame the queue could not hold is superseded rather
		// than lost.
		//
		// WHICH bound refused it decides where an operator looks, because the
		// fixes differ: a full park buffer is a slow client, a full budget is an
		// undersized deployment, and a frame past the whole budget is one only
		// an operator can clear. One log line naming the park buffer for all
		// three sent them chasing a slow client while the pool sat at capacity.
		slog.Warn(parkRefusalMessage(refusal.bound),
			"user_id", user.ID, "client_id", clientID, "bound", refusal.bound)
		return
	}
	// STEADY STATE: there is no scan to fall back from, so drop the subscriber
	// rather than block the manager goroutine. Cancelling ctx exits the writer
	// loop and fires the deferred unsub, so later broadcasts skip us.
	//
	// The bound is logged rather than baked into the message, for the reason the
	// parking arm distinguishes them: "the client is behind" and "the Hub is out
	// of shared memory" are read by different people and fixed in different
	// places, and this arm could not tell them apart at all until the refusal
	// started carrying it.
	slog.Warn("userevents: subscriber buffer full, dropping connection",
		"user_id", user.ID, "client_id", clientID, "bound", refusal.bound)
	cancel()
}

// parkRefusalMessage names the bound that refused a frame during the parking
// phase, in the terms the reader of the log line needs. The three have
// different fixes and only one of them is the client's, so they cannot share a
// sentence.
func parkRefusalMessage(bound dropBound) string {
	switch bound {
	case dropBoundBytes:
		return "userevents: shared queue memory is full while building the bootstrap frame, falling back to a snapshot"
	case dropBoundCapacity:
		return "userevents: a broadcast frame is larger than the entire user-events queue budget, falling back to a snapshot; " +
			"raise userevents_queue_memory_budget"
	case dropBoundFrames:
		return "userevents: park buffer full while building the bootstrap frame, falling back to a snapshot"
	}
	// No default arm, so a fourth bound stops compiling here rather than being
	// described by whichever sentence happened to be last.
	return "userevents: a frame was refused while building the bootstrap frame, falling back to a snapshot"
}

// userEventsRequest is everything the URL of a /ws/userevents connect carries.
// Tenancy is deliberately absent: it is the authenticated user, never a query
// parameter (see parseUserEventsRequest).
type userEventsRequest struct {
	// workspaceIDs is the requested filter, in URL order. Empty means "every
	// workspace this user can see".
	workspaceIDs []string
	// requested is the same set, in the shape crdt.Subscriber wants. Nil rather
	// than empty when the filter is unnarrowed, which is what the manager reads
	// as "no narrowing".
	requested map[string]bool
	// resumeCursor and resumeEpoch are the reconnect cursor. Nil cursor is a
	// first connect, which SubscribeWithACL answers with a full snapshot.
	resumeCursor *leapmuxv1.HLC
	resumeEpoch  int64
}

// parseUserEventsRequest reads and validates the connect's query parameters.
//
// Extracted from ServeHTTP because it is the one part of a 300-line handler
// that is a pure function of the request, and it carries two rejections whose
// reasoning matters more than their code. Both failures are 400s the caller
// reports identically, so the handler is left with one arm rather than two.
func parseUserEventsRequest(r *http.Request) (userEventsRequest, error) {
	// Tenancy is the authenticated user. No client-supplied user_id query
	// parameter: channelwire.UserEventsURL encodes only workspace_ids and the
	// resume cursor (resume_after_hlc / resume_epoch), and accepting a foreign
	// id would let any authenticated user drive registry.Get (which performs no
	// authorization) into bootstrapping an arbitrary tenant's CRDT Manager.
	var req userEventsRequest
	if raw := r.URL.Query().Get("workspace_ids"); raw != "" {
		for _, w := range strings.Split(raw, ",") {
			if w = strings.TrimSpace(w); w != "" {
				req.workspaceIDs = append(req.workspaceIDs, w)
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
	resumeCursor, resumeEpoch, err := channelwire.ParseResumeCursorFromQuery(r.URL.Query())
	if err != nil {
		return userEventsRequest{}, err
	}
	req.resumeCursor, req.resumeEpoch = resumeCursor, resumeEpoch
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
	// controlipc's hub_stream and the control CLI both pass their workspace ids
	// with a nil cursor -- so this narrows the contract without breaking a
	// caller. Permitting a SAFE narrowed resume needs the mint-time filter
	// carried alongside the cursor and re-checked here, which this deliberately
	// leaves open rather than pre-empting.
	if len(req.workspaceIDs) > 0 && req.resumeCursor != nil {
		return userEventsRequest{}, errors.New("resume_after_hlc is not accepted with workspace_ids: a cursor minted under a " +
			"narrower filter cannot be replayed under a wider one")
	}
	if len(req.workspaceIDs) > 0 {
		req.requested = make(map[string]bool, len(req.workspaceIDs))
		for _, workspaceID := range req.workspaceIDs {
			req.requested[workspaceID] = true
		}
	}
	return req, nil
}

func (h *UserEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := h.authenticate(r)
	if err != nil {
		writeHTTPAuthError(w, "user events", err)
		return
	}

	userID := user.ID.String()
	req, err := parseUserEventsRequest(r)
	if err != nil {
		countConnectWithoutBootstrap()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mgr, err := h.registry.Get(r.Context(), userID)
	if err != nil {
		countConnectWithoutBootstrap()
		http.Error(w, "registry get: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Per-message read limit so a malformed client can't blow our memory.
	// WatchUserEvent payloads are bounded by the protobuf envelope. Named rather
	// than repeated as a literal so this socket and the subscribers reading from
	// it cannot drift apart: the two matching was previously only a claim in a
	// comment.
	wsConn, ok := h.acceptWS(w, r, endpointUserEvents, "userevents-relay", channelwire.UserEventsReadLimit, user)
	if !ok {
		countConnectWithoutBootstrap()
		return
	}

	// Counted on every refusal, whichever reason: the connect series is a
	// complete partition of authenticated connects, and a connection turned away
	// at the cap is as much a connect outcome as one turned away for a revoked
	// credential. bind has already closed the socket with the reason.
	ctx, cleanupLease, outcome := h.authLease.bind(r.Context(), user, wsConn)
	if outcome != auth.LeaseGranted {
		countConnectWithoutBootstrap()
		return
	}
	defer cleanupLease()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Derived ONCE, and used both to register and to log. Recomputing it per log
	// site let a line name an identity the registered subscriber does not have
	// if the derivation ever grew a condition.
	clientID := presenceClientID(user)
	sub, queue := newUserEventsSubscriber(ctx, cancel, user, clientID, req.requested, h.queuePool)
	// Registered BEFORE the unsub below, so LIFO runs it AFTER: a broadcast that
	// snapshot the subscriber set before it was unregistered can still be inside
	// Send when unsub returns, and closing first would race that frame's charge.
	// Everything still queued is released here and the queue leaves the pool --
	// the connection's whole share of the shared budget, returned on every exit
	// path including the early ones below.
	defer queue.Close()

	// Take a build slot before anything starts materializing a snapshot, and
	// give it back the moment the frame has been charged or refused -- NOT when
	// the connection ends, or the gate would bound live subscribers instead of
	// concurrent builds.
	//
	// Non-blocking: parking here would hold an upgraded socket and its lease
	// slot while it waited, turning a memory bound into a connection-cap one.
	// The answer is the retry-later the client already handles, and it is given
	// BEFORE the allocation rather than after -- which is the whole point, and
	// the thing the byte charge structurally cannot do.
	select {
	case h.bootstrapGate <- struct{}{}:
	default:
		// Counted like any other authenticated connect that never reached a
		// bootstrap arm, so the subscribe metric stays complete.
		countConnectWithoutBootstrap()
		slog.Warn("userevents: too many bootstrap frames already being built, asking the client to retry the connect",
			"user_id", user.ID, "client_id", clientID, "concurrent_builds", cap(h.bootstrapGate))
		_ = wsConn.Close(websocket.StatusTryAgainLater, "temporarily unavailable, retry")
		return
	}
	// Deferred as well as called explicitly below: the explicit release is what
	// makes the gate bound BUILDS, and the defer is what guarantees it is
	// released at all when a path between here and there returns early. sync.Once
	// makes the pair safe rather than a double-release that would inflate the
	// gate's capacity for the life of the process.
	var releaseBuild sync.Once
	endBuild := func() { releaseBuild.Do(func() { <-h.bootstrapGate }) }
	defer endBuild()

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
		return resolveAllowedWorkspacesSetForUser(ctx, h.store, req.workspaceIDs, user)
	}
	subscribeStart := time.Now()
	out, err := mgr.SubscribeWithACL(ctx, sub, req.resumeCursor, req.resumeEpoch, resolve)
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
			_ = wsConn.Close(websocket.StatusPolicyViolation, channelwire.CloseReasonForbidden)
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
	// Charged for the duration of the write. This frame is the largest single
	// thing the connection holds and it is unique to this subscriber, so N tabs
	// reconnecting at once really is N snapshots resident at once -- the same
	// per-connection-with-no-process-bound shape the backlog path just lost.
	//
	// Refusing is a real option here, unlike on /ws/channel: the socket is
	// already upgraded, so the Hub can close with a code the client reads as
	// recoverable and retries, which is the same answer it gives a transient
	// store failure above. Serving the connect anyway is what the Hub cannot
	// afford, because a reconnect storm is precisely when it would.
	admission := queue.AdmitBootstrap(bootstrapEvt)
	// The frame exists and the budget has answered for it, so the build window
	// is over on every arm below -- including the refusals, whose whole purpose
	// is to let the next builder in.
	endBuild()
	switch admission {
	case bootstrapNotAdmissible:
		// The connection is already gone, or this is a second opening frame --
		// a teardown race or a bug, either way not a memory verdict. Say nothing
		// on the memory metric and let the deferred cleanup run.
		slog.Debug("userevents: bootstrap not admissible", "user_id", user.ID, "client_id", clientID)
		return
	case bootstrapUnfittable:
		// Bigger than the WHOLE pool, so every occupancy refuses it and no retry
		// can ever succeed. Answering with retry-later put the client in a loop
		// that rebuilt this same snapshot every few seconds forever, invisible
		// except as an app that never finished loading. Close terminally with a
		// reason the browser turns into operator-actionable copy.
		slog.Error("userevents: the opening snapshot exceeds the entire user-events queue budget; "+
			"raise userevents_queue_memory_budget",
			"user_id", user.ID, "client_id", clientID, "mode", out.Mode(),
			"frame_charged_bytes", bootstrapEvt.Size(),
			"frame_wire_bytes", bootstrapEvt.WireSize(),
			"pool_capacity", h.queuePool.Capacity())
		// `capacity`, NOT `bytes`. The two refusals below it are one phase and
		// two entirely different conditions: this one is refused on an empty
		// pool just as readily, so an operator who saw it as memory pressure
		// would watch leapmux_sendq_pool_used_bytes for a spike that never
		// comes, on a dashboard cell that never clears until the budget is
		// raised. The enum bootstrapAdmission exists precisely to keep these
		// apart for the client; sharing one series left the operator's half of
		// that exactly as broken as the doc says it was.
		//
		// Counted by AdmitBootstrap, from the same admission outcome that chose
		// this arm. Re-deriving the label here was a second derivation of one
		// event, which is the split refuseFrame exists to close.
		_ = wsConn.Close(websocket.StatusPolicyViolation, channelwire.CloseReasonSnapshotTooLarge)
		return
	case bootstrapPoolFull:
		slog.Warn("userevents: shared queue memory is full, asking the client to retry the connect",
			"user_id", user.ID, "client_id", clientID, "mode", out.Mode(),
			// Both, because they answer different questions: the charge is why
			// this connect was refused, the wire size is what the client was
			// going to receive.
			"frame_charged_bytes", bootstrapEvt.Size(),
			"frame_wire_bytes", bootstrapEvt.WireSize(),
			"pool_used", h.queuePool.Used(), "pool_capacity", h.queuePool.Capacity())
		_ = wsConn.Close(websocket.StatusTryAgainLater, "temporarily unavailable, retry")
		return
	case bootstrapAdmitted:
	}
	writeErr := writeUserEvent(ctx, wsConn, bootstrapEvt)
	// Refunded on both outcomes: a failed write stopped holding the frame too.
	queue.BootstrapSent()
	if writeErr != nil {
		slog.Debug("userevents: write bootstrap failed", "user_id", user.ID, "mode", out.Mode(), "error", writeErr)
		return
	}

	// Open the bounded live queue. Frames that parked during the subscribe/scan
	// window (before this writer loop) are handed back first by Next, so the
	// wire order is bootstrap frame (above) → parked live catch-up →
	// steady-state live queue.
	if dropped := queue.Bootstrapped(); dropped {
		// The park buffer overflowed after the manager's last Overflowed()
		// check -- i.e. during the bootstrap WRITE, which on a large account is
		// the longest stretch of the parking window. Flushing what survived
		// would leave this connection permanently short of whatever was
		// dropped, silently and for its whole session, because the fan-out
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

	// Reads start HERE, and so does the keepalive, in one call that cannot give
	// one without the other.
	//
	// The reads are discarded: clients send nothing after the initial URL query,
	// and the loop is there so the library observes a close frame promptly when
	// the peer disconnects. The probe is what notices a peer that vanishes
	// WITHOUT one -- every other bound on this socket fires on a write.
	//
	// Not one line earlier. Everything above is the pre-bootstrap window -- the
	// ACL resolve, the resume scan or baseline walk, the snapshot marshal, and a
	// write bounded only by relayWriteTimeout -- and nothing in it reads the
	// socket. coder/websocket processes a pong on the read path, so a probe
	// armed up there could not see its answer: it would time out and cancel a
	// perfectly healthy connection mid-bootstrap, which on a large account is
	// exactly the reconnect storm this endpoint has to survive.
	h.keepalive.startDrainingReads(ctx, wsConn, cancel, endpointUserEvents, userID)

	for {
		evt, ok := queue.Next(ctx)
		if !ok {
			return
		}
		if err := writeUserEvent(ctx, wsConn, evt); err != nil {
			slog.Debug("userevents: write event failed", "user_id", user.ID, "error", err)
			return
		}
	}
}

// writeUserEvent serializes evt into a length-prefixed binary frame on
// the WS via channelwire.WriteFramedBytes (the same framing
// /ws/channel uses, so a single client-side read helper handles
// both relays).
//
// The MarshaledEvent's lazy Bytes() cache means broadcasts shared
// across N subscribers pay the proto.Marshal cost ONCE: whichever
// subscriber queues the frame first fills the cache (sizing it for the
// budget is what forces it) and everyone else reuses the result.
// Each write is bounded by relayWriteTimeout, the same per-write budget
// the channel relay applies.
//
// This socket does NOT need the channel relay's queue: the broadcaster
// already feeds it through subscriberQueue, which drops and cancels
// rather than blocking, and which charges its own bytes against a shared
// sendq.Pool -- so the backlog is bounded here in both frames and memory,
// and a relayWriter would only queue behind a queue.
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
