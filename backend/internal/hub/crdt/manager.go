package crdt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"google.golang.org/protobuf/proto"
)

const (
	// EpochDuration is how long the manager runs on a single epoch
	// before advancing. 14 days matches the dedup retention window;
	// retries older than 2 epochs are rejected as stale_epoch.
	EpochDuration = 14 * 24 * time.Hour
	// HubReservedPrincipal is the reserved principal_id stamped on
	// hub-driven internal ops (lifecycle, worker reconciliation
	// tombstones). Doubles as the prefix for origin_client_id stamps
	// produced by the manager (see hubClientID in NewManager).
	HubReservedPrincipal = "hub"
	// DedupTTL is how long a batch_id stays in org_recent_batch_ids.
	DedupTTL = 14 * 24 * time.Hour
	// PresenceClearGrace is how long the manager waits after a
	// client's last WS subscription closes before clearing its
	// presence entries. A reconnect within the grace window cancels
	// the pending clear so brief network blips don't flicker the
	// active-client gate.
	PresenceClearGrace = 60 * time.Second
	// PresenceEvictAfter is the inactivity threshold used by the
	// runPresence sweep ticker. Defense-in-depth only: normal
	// disconnects clear presence within PresenceClearGrace via
	// RemoveClient. The sweep catches entries orphaned by abnormal
	// teardowns (panicked goroutine partway through unsubscribe, lost
	// clearCh job, etc.) so a misbehaving client cannot leak presence
	// rows indefinitely.
	PresenceEvictAfter = 24 * time.Hour
	// presenceSweepInterval drives PresenceEvictAfter. Cheap pass
	// (one mutex + map walk) so it can run frequently — once an hour
	// keeps the worst-case orphan lifetime predictable without adding
	// observable load.
	presenceSweepInterval = time.Hour
)

// SubmitInput is what callers hand the manager. internal=true skips
// per-op auth and is required for SetWorkspaceRootNodeOp.
type SubmitInput struct {
	OrgID        string
	Epoch        int64
	Batches      []*leapmuxv1.OpBatch
	PrincipalID  string
	OriginClient string
	// WorkspaceScopeID, when non-empty, narrows client-auth checks to
	// this single workspace. Delegation bearers use it so a principal
	// with broader owner/share grants cannot submit ops against a
	// sibling workspace through a scoped remote session.
	WorkspaceScopeID string
	Internal         bool
}

// Manager owns one org's CRDT state and its journal, and coordinates
// presence + subscriber concerns through two dedicated controllers.
// All state writes funnel through the goroutine started by Start;
// methods that mutate state are NOT safe to call from outside.
//
// The HLC `clock` is the single canonical stream — every
// committed op (client-driven OR hub-internal lifecycle / worker
// reconciliation tombstone) gets its canonical HLC from this clock,
// so the (physical, logical, client_id) tuple is monotonic across
// op sources. `hubClientID` is the value stamped on
// origin_client_id for hub-internal submits.
//
// Why two controllers? `subscribers` owns the set of attached
// listeners + a lock-free snapshot for the broadcast hot path;
// `presenceCtl` owns the heartbeat tracker, per-client refcount, and
// deferred-clear timers. Splitting them into named types narrows the
// lock contention surface (m.mu now protects state only) and makes
// the "Subscribe touches both" sequence explicit at every call site.
type Manager struct {
	orgID       string
	clock       *Clock
	hubClientID string

	mu    sync.RWMutex
	state *leapmuxv1.OrgCrdtState
	// stateWriteMu serializes every writer of m.state against each other: the
	// manager goroutine's read-modify-swap in processSubmit / maybeAdvanceEpoch
	// and MutateInternal's in-place write from the lifecycle-consumer request
	// goroutine. The manager goroutine reads m.state's maps BARE (it is normally
	// the sole writer), so without this an out-of-band MutateInternal add/delete of
	// a workspace would race those bare reads in ValidateBatch / DiffProjectionForBatch
	// -- a Go-fatal "concurrent map read and map write" that crashes the Hub -- and
	// could be clobbered by the commit swap. m.mu still guards the maps against the
	// RLock readers (Materialized, currentEpoch, broadcast). Lock order: stateWriteMu
	// is outermost (stateWriteMu -> projection -> m.mu); nothing takes projection or
	// m.mu and then stateWriteMu, so there is no inversion.
	stateWriteMu sync.Mutex
	subscribers  *SubscriberController
	projection   sync.Mutex
	// aclTransitionMu serializes whole workspace ACL transitions
	// (CommitWorkspaceAccessTransition) end to end, so two shares of the same
	// workspace cannot interleave their revoke/commit/grant phases and let the
	// in-memory filter order diverge from the DB-commit order. It is taken OUTSIDE
	// projection (aclTransitionMu -> projection -> m.mu) and, unlike projection, is
	// NOT held across the DB commit's op-broadcast-blocking window -- only this one
	// call site takes it, and it never takes projection before it, so there is no
	// inversion.
	aclTransitionMu sync.Mutex
	presenceCtl     *PresenceController
	auth            AuthChecker
	journal         Journal
	now             func() time.Time
	logger          *slog.Logger

	// clearGrace seeds presenceCtl at construction. Held on Manager
	// (rather than only inside the controller) so WithPresenceClearGrace
	// can mutate it before NewManager finishes building the controller.
	clearGrace time.Duration

	submitCh   chan submitJob
	internalCh chan submitJob
	stop       chan struct{}
	done       chan struct{}

	// activity guards lastActivity. Kept on its own mutex (rather than
	// piggybacking m.mu) so the registry's idle-eviction janitor can
	// read it without contending on the broadcast path's RLock.
	activity     sync.Mutex
	lastActivity time.Time

	// auditWG tracks background audit goroutines (currently just
	// auditOrphanTabTombstones). Stop() waits on this so audits in
	// flight at shutdown still get the chance to emit their log
	// breadcrumb instead of being silently dropped.
	auditWG sync.WaitGroup
}

// SubscriberFilter narrows the events a subscriber receives.
type SubscriberFilter struct {
	WorkspaceIDs map[string]bool
}

// IsAllowed returns true when the workspace passes the filter.
// nil WorkspaceIDs means "allow all"; a non-nil empty map means
// "allow none". The hub computes this set per connection by
// intersecting with the caller's read ACL.
func (f SubscriberFilter) IsAllowed(workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	if f.WorkspaceIDs == nil {
		return true
	}
	return f.WorkspaceIDs[workspaceID]
}

// Subscriber is a single open org-event subscription (one connected
// `/ws/orgevents` client, or a one-shot in-process test reader).
// Events are pushed via Send; the caller owns the underlying stream's
// lifetime.
//
// ClientID is the namespaced presence identity derived from a cookie
// session, bearer kind and token id, or user id (see
// `service.presenceClientID`). It scopes the
// refcount the manager keeps for deferred presence clearing on
// disconnect. Empty disables presence tracking for this subscription
// (e.g. server-internal subscribers).
type Subscriber struct {
	UserID   string
	ClientID string
	// RequestedWorkspaceIDs is the immutable upper bound selected by an
	// explicit workspace_ids subscription query. nil means the subscriber
	// requested all readable workspaces; a non-nil map never expands beyond
	// those IDs when ACLs change.
	RequestedWorkspaceIDs map[string]bool
	// WorkspaceScopeID is the immutable upper bound on Filter for the
	// lifetime of the subscription. Delegation-authenticated streams set
	// it to the workspace encoded in the bearer; lifecycle ACL refreshes
	// may add that workspace but must never add a sibling.
	WorkspaceScopeID string
	Filter           SubscriberFilter
	// Send delivers one event to this subscriber.
	//
	// Contract: Send MUST return promptly — implementations either push
	// to a bounded per-subscriber buffer with a non-blocking select
	// (returning ErrSubscriberSlow when full so the subscriber can be
	// torn down) or do the work synchronously. Send MUST NOT block on
	// network IO or a full unbounded buffer; the manager's broadcast
	// goroutine fans out to every subscriber sequentially, so one slow
	// Send would head-of-line block the entire org's broadcasts.
	//
	// Manager dedupes the *MarshaledEvent pointer across subscribers
	// that receive the same underlying *leapmuxv1.WatchOrgEvent, so
	// callers that marshal the payload (e.g. ws_orgevents.go) can call
	// `evt.Bytes()` and pay the proto.Marshal cost once per broadcast —
	// not once per subscriber.
	Send func(*MarshaledEvent) error
}

func subscriberMaySeeWorkspace(sub *Subscriber, workspaceID string) bool {
	if sub.WorkspaceScopeID != "" && sub.WorkspaceScopeID != workspaceID {
		return false
	}
	return sub.RequestedWorkspaceIDs == nil || sub.RequestedWorkspaceIDs[workspaceID]
}

// ErrSubscriberSlow signals that a subscriber's bounded send buffer is
// full and the subscriber should be torn down rather than waited on.
// Callers' Send implementations return this when their internal queue
// can't accept another event without blocking; the WS handler treats
// it as a fatal signal that cancels the per-subscriber context and
// drops the connection.
var ErrSubscriberSlow = errors.New("crdt: subscriber send buffer full")

// MarshaledEvent wraps a `*leapmuxv1.WatchOrgEvent` with a lazy
// proto.Marshal cache. Multiple subscribers share the same
// `*MarshaledEvent` for events the manager broadcasts to all of them;
// the first writer that calls `Bytes()` pays the marshal cost and
// subsequent writers reuse the cached buffer.
//
// The wrapper is intentionally minimal: callers that only need to
// inspect the proto can read `evt.Event` directly. Callers writing
// to a wire should call `evt.Bytes()`.
type MarshaledEvent struct {
	// Event is the underlying proto. Read-only for consumers; the
	// manager constructs it before any Send call sees the wrapper.
	Event *leapmuxv1.WatchOrgEvent

	once  sync.Once
	bytes []byte
	err   error
}

// NewMarshaledEvent wraps `evt` for delivery. The proto pointer is
// captured by reference; do not mutate `evt` after constructing the
// wrapper.
func NewMarshaledEvent(evt *leapmuxv1.WatchOrgEvent) *MarshaledEvent {
	return &MarshaledEvent{Event: evt}
}

// Bytes returns the binary-marshaled representation of the wrapped
// event. The first caller across all subscribers pays the marshal
// cost; subsequent callers receive the cached buffer (and the cached
// error, if marshal failed).
func (e *MarshaledEvent) Bytes() ([]byte, error) {
	e.once.Do(func() {
		e.bytes, e.err = proto.Marshal(e.Event)
	})
	return e.bytes, e.err
}

// submitJob carries one SubmitOps request through the goroutine.
type submitJob struct {
	input    SubmitInput
	respCh   chan submitResponse
	internal bool
}

type submitResponse struct {
	results []*leapmuxv1.BatchResult
	err     error
}

type presenceJob struct {
	workspaceID string
	clientID    string
}

// presenceClearJob is scheduled by the deferred-clear timer after a
// client's last WS subscription drops. The manager loop processes it
// and, if the client hasn't reconnected in the meantime, evicts every
// presence entry for that client_id.
type presenceClearJob struct {
	clientID string
}

// ManagerOption tunes optional behavior of a Manager at construction
// time. Used today only to override timings in tests; new knobs that
// don't belong on the required-args list can be added here.
type ManagerOption func(*Manager)

// WithPresenceClearGrace overrides the deferred presence-clear grace
// window. Tests use this to keep the grace short (tens of ms) so they
// don't have to sleep through the production default
// (`PresenceClearGrace`, 60 s).
func WithPresenceClearGrace(d time.Duration) ManagerOption {
	return func(m *Manager) { m.clearGrace = d }
}

// NewManager constructs a manager. Callers MUST call Bootstrap before
// Start so the in-memory state is consistent with disk.
//
// The lifecycle outbox reader is passed per-call to SubmitLifecycle
// instead of being stashed on the manager — the manager only ever
// reads from one when the service-layer drains pending rows, and
// holding a reference here served no purpose.
func NewManager(orgID string, journal Journal, auth AuthChecker, logger *slog.Logger, now func() time.Time, opts ...ManagerOption) *Manager {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	hubID := HubReservedPrincipal + "-" + orgID[:min(8, len(orgID))]
	m := &Manager{
		orgID:       orgID,
		clock:       NewClock("hub-canonical"),
		hubClientID: hubID,
		auth:        auth,
		journal:     journal,
		now:         now,
		logger:      logger.With("org_id", orgID),
		clearGrace:  PresenceClearGrace,
		submitCh:    make(chan submitJob, 64),
		internalCh:  make(chan submitJob, 16),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		subscribers: newSubscriberController(),
	}
	for _, opt := range opts {
		opt(m)
	}
	m.presenceCtl = newPresenceController(now, m.clearGrace, m.stop)
	return m
}

// OrgID returns the manager's org id.
func (m *Manager) OrgID() string { return m.orgID }

// Bootstrap loads org_state + replays org_op_batches > watermark. Safe
// to call before Start.
func (m *Manager) Bootstrap(ctx context.Context) error {
	state, tail, err := m.journal.LoadState(ctx, m.orgID)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state == nil {
		state = NewState(m.orgID)
		state.EpochStartedAt = nil
	}
	ensureStateMaps(state)
	for _, batch := range tail {
		for _, op := range batch.GetOps() {
			Apply(state, op)
		}
	}
	m.clock.Observe(state.GetMaxHlc())
	m.state = state
	// Stamp activity so a freshly-bootstrapped manager isn't eligible
	// for immediate eviction. The registry's idle-eviction window
	// applies from the bootstrap moment onward; without this, a manager
	// loaded at t=0 with no traffic would look eternally idle (lastActivity
	// zero-value) and the janitor would tear it down on the next sweep.
	m.touchActivity()
	return nil
}

// Start begins serving submit jobs. Blocks; call from a goroutine.
//
// Internally Start runs two goroutines:
//   - the main loop (this function) owns submitCh, internalCh, and the
//     housekeeping ticker — every entry that mutates m.state or talks
//     to the journal lives here, so a slow DB commit only stalls other
//     submits, not presence.
//   - a presence loop (runPresence) owns presenceCh + clearCh — these
//     paths access m.presence, m.clearTimers, and m.subs (all under
//     their own locks) but never touch m.state, so they can run
//     concurrently with the main loop without further synchronization.
//
// Stop() closes m.stop; both loops exit on that signal, and the
// deferred wait below ensures the presence loop is fully torn down
// before the main loop's close(m.done) wakes Stop's waiter.
func (m *Manager) Start(ctx context.Context) error {
	defer close(m.done)
	presenceExited := make(chan struct{})
	go func() {
		defer close(presenceExited)
		m.presenceCtl.Run(ctx, m.broadcastPresence)
	}()
	defer func() { <-presenceExited }()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.stop:
			return nil
		case job := <-m.submitCh:
			job.respCh <- m.processSubmit(ctx, job)
		case job := <-m.internalCh:
			job.respCh <- m.processSubmit(ctx, job)
		case <-ticker.C:
			m.tickHousekeeping(ctx)
		}
	}
}

// touchActivity stamps the manager as freshly active. Called on every
// Submit / SubmitInternal entry point so the Registry's idle-eviction
// janitor only stops managers that genuinely haven't seen traffic.
func (m *Manager) touchActivity() {
	m.activity.Lock()
	m.lastActivity = m.now()
	m.activity.Unlock()
}

// idleSince reports the manager's last-known activity time and whether
// it currently has any live subscribers or presence-tracked clients.
// The Registry combines both to decide whether eviction is safe.
func (m *Manager) idleSince() (lastActivity time.Time, hasLiveAttachments bool) {
	hasLiveAttachments = m.subscribers.Len() > 0 || m.presenceCtl.HasLiveConnections()
	m.activity.Lock()
	lastActivity = m.lastActivity
	m.activity.Unlock()
	return lastActivity, hasLiveAttachments
}

// WaitForAudits blocks until any in-flight background audit
// goroutines spawned by processBatch have emitted their log
// breadcrumb. Production code relies on Stop() to drain at shutdown;
// tests that assert against the audit log call this after Submit so
// the assertion isn't racy against the async goroutine.
func (m *Manager) WaitForAudits() {
	m.auditWG.Wait()
}

// Stop signals the goroutine to exit and waits for it. Any pending
// deferred-clear timers are stopped so they don't fire on a defunct
// manager. Background audit goroutines are drained before returning
// so their log breadcrumbs always make it out.
func (m *Manager) Stop() {
	select {
	case <-m.stop:
		return
	default:
	}
	close(m.stop)
	<-m.done
	m.presenceCtl.Shutdown()
	m.auditWG.Wait()
}

// Submit is the client-callable entrypoint. Routed through the
// goroutine.
func (m *Manager) Submit(ctx context.Context, input SubmitInput) ([]*leapmuxv1.BatchResult, error) {
	resp := make(chan submitResponse, 1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m.submitCh <- submitJob{input: input, respCh: resp}:
	}
	m.touchActivity()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resp:
		return r.results, r.err
	}
}

// SubmitInternal is the in-process Go API for hub-driven ops
// (lifecycle, worker reconciliation tombstones). The internal flag
// skips the per-op auth check and gates SetWorkspaceRootNodeOp.
func (m *Manager) SubmitInternal(ctx context.Context, input SubmitInput) ([]*leapmuxv1.BatchResult, error) {
	input.Internal = true
	if input.PrincipalID == "" {
		input.PrincipalID = HubReservedPrincipal
	}
	if input.OriginClient == "" {
		input.OriginClient = m.hubClientID
	}
	resp := make(chan submitResponse, 1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m.internalCh <- submitJob{input: input, respCh: resp, internal: true}:
	}
	m.touchActivity()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resp:
		return r.results, r.err
	}
}

// HeartbeatPresence is the client-callable entrypoint for
// UpdatePresence.
func (m *Manager) HeartbeatPresence(ctx context.Context, workspaceID, clientID string) error {
	return m.presenceCtl.PostHeartbeat(ctx, workspaceID, clientID)
}

// SubscribeWithACL resolves a subscriber's workspace filter and registers it
// atomically under aclTransitionMu, closing the resolve-then-register TOCTOU.
//
// resolve reads the DB ACL and returns the allowed workspace set; it runs while
// this holds aclTransitionMu, the SAME lock CommitWorkspaceAccessTransition holds
// across a share/unshare. That serialization means a concurrent transition of a
// workspace in the filter is ordered either FULLY before resolve() -- so resolve
// reads the post-commit ACL and the workspace is already excluded -- or FULLY
// after this Add -- so the transition's revoke pass sees the now-registered
// subscriber. Without it, a filter resolved from the pre-commit ACL could be
// Added after the whole transition finished, and the post-commit reclassify (which
// only visits already-registered subscribers) would miss it, leaking a
// just-unshared workspace's snapshot and ops until the subscriber reconnected.
//
// resolve's error is returned unregistered so the caller can reject the connection
// before streaming any events. Lock order aclTransitionMu -> projection -> m.mu is
// preserved because Subscribe (called here) takes only projection and m.mu.
func (m *Manager) SubscribeWithACL(sub *Subscriber, resolve func() (map[string]bool, error)) (initial *leapmuxv1.OrgMaterialized, unsub func(), err error) {
	m.aclTransitionMu.Lock()
	defer m.aclTransitionMu.Unlock()
	allowed, err := resolve()
	if err != nil {
		return nil, nil, err
	}
	sub.Filter.WorkspaceIDs = allowed
	initial, unsub = m.Subscribe(sub)
	return initial, unsub, nil
}

// Subscribe attaches a new subscriber. Returns an unsubscribe
// callback. Bootstrap is sent inline (the caller's stream layer
// formats it).
//
// The production org-events path registers through SubscribeWithACL, which
// resolves the filter and calls this Add while holding aclTransitionMu so a
// concurrent ACL transition cannot leave a straggler with a stale pre-commit
// filter (see SubscribeWithACL). A direct Subscribe (tests, or a caller that has
// already resolved a filter under no such lock) does NOT get that guarantee.
//
// Subscribers with a non-empty ClientID contribute to a refcount keyed
// on that id. The first Subscribe cancels any pending deferred clear;
// the last unsub schedules one PresenceClearGrace into the future. A
// reconnect inside the grace window keeps the client's presence
// entries intact so the active-client gate doesn't flicker.
func (m *Manager) Subscribe(sub *Subscriber) (initial *leapmuxv1.OrgMaterialized, unsub func()) {
	// The whole sequence runs under m.projection -- the same lock broadcasts and
	// each visibility phase of an ACL transition (share/unshare) take -- so a
	// subscriber's registration + initial snapshot are atomic with respect to any
	// concurrent broadcast and any single grant/revoke phase: the filter captured
	// by Add and the materialized baseline can never straddle a filter mutation.
	// (CommitWorkspaceAccessTransition applies revokes and grants in separate
	// projection-held phases around a lock-free DB commit. A subscription whose Add
	// lands DURING that transition is re-classified by its post-commit phase; a
	// subscription serialized against the transition via SubscribeWithACL resolves
	// the correct pre- or post-commit ACL. The post-commit reclassify only visits
	// already-registered subscribers, which is why the production path must go
	// through SubscribeWithACL rather than resolving a filter and Adding it here
	// unserialized.) The cost is
	// that the O(N) materializedLocked clone below is serialized against broadcasts
	// for that duration (a large-org connect briefly stalls the org's commit/
	// broadcast pipeline); relaxing it -- computing the snapshot outside
	// m.projection -- would reopen exactly that straddle window, so it is held
	// deliberately.
	//
	// Within that, m.mu is only RLocked for the clone. materializedLocked walks
	// every visible node/tab/floating-window for the subscriber's filter; taking
	// the state write lock would block every concurrent commit, whereas the RLock
	// only contends the brief state-swap. Subscriber visibility is identical
	// either way: by the time we take the state RLock the subscriber is in
	// subscribers, so it sees the next broadcast, and the initial snapshot
	// computed under RLock is strictly newer-than-or-equal to whatever a commit
	// that lost the race would have produced.
	m.projection.Lock()
	initialFilter := m.subscribers.Add(sub)
	m.presenceCtl.OnConnect(sub.ClientID)

	m.mu.RLock()
	initial = m.materializedLocked(initialFilter)
	m.mu.RUnlock()
	m.projection.Unlock()
	// Idempotent unsub: a caller that invokes it twice (an error-path cleanup
	// racing a deferred cleanup, or the partway-through-unsubscribe teardown the
	// registry hardens against) must not decrement the presence refcount twice --
	// that would underflow the count and prematurely clear a client that still has
	// live connections, flickering the active-client gate.
	var unsubOnce sync.Once
	return initial, func() {
		unsubOnce.Do(func() {
			m.subscribers.Remove(sub)
			m.presenceCtl.OnDisconnect(sub.ClientID)
		})
	}
}

// Materialized returns the public projection filtered to a given
// allowed-set (used by tests / one-shot callers).
func (m *Manager) Materialized(filter SubscriberFilter) *leapmuxv1.OrgMaterialized {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.materializedLocked(filter)
}

func (m *Manager) materializedLocked(filter SubscriberFilter) *leapmuxv1.OrgMaterialized {
	out := &leapmuxv1.OrgMaterialized{
		OrgId:           m.state.GetOrgId(),
		Nodes:           map[string]*leapmuxv1.NodeRecord{},
		Tabs:            map[string]*leapmuxv1.TabRecord{},
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{},
		Workspaces:      map[string]*leapmuxv1.WorkspaceContentsRecord{},
		MaxHlc:          HLCClone(m.state.GetMaxHlc()),
		CurrentEpoch:    m.state.GetCurrentEpoch(),
	}
	roots := registeredRoots(m.state)
	// Build node→workspace once via BFS from each filter-allowed root,
	// avoiding the per-entry O(depth) walks that nodeWorkspace /
	// resolveTileWorkspace would otherwise do for every node and every
	// tab. Tombstoned ancestors are not skipped during descent — this
	// preserves nodeWorkspace's existing behaviour where a live node
	// whose intermediate ancestor is tombstoned still resolves to the
	// registered-root workspace above it.
	nodeWS := buildNodeWorkspaceMap(m.state, roots, filter)

	for wsID, ws := range m.state.GetWorkspaces() {
		if !filter.IsAllowed(wsID) {
			continue
		}
		out.Workspaces[wsID] = &leapmuxv1.WorkspaceContentsRecord{
			WorkspaceId: ws.GetWorkspaceId(),
			RootNodeId:  ws.GetRootNodeId(),
		}
	}
	for id, n := range m.state.GetNodes() {
		if !HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		if _, ok := nodeWS[id]; ok {
			out.Nodes[id] = cloneNode(n)
		}
	}
	for id, t := range m.state.GetTabs() {
		if _, ok := nodeWS[t.GetTileId().GetValue()]; ok {
			out.Tabs[id] = cloneTab(t)
		}
	}
	for id, fw := range m.state.GetFloatingWindows() {
		ws := fw.GetWorkspaceId().GetValue()
		if ws != "" && filter.IsAllowed(ws) {
			out.FloatingWindows[id] = cloneFloatingWindow(fw)
		}
	}
	return out
}

// buildNodeWorkspaceMap returns `node_id → workspace_id` for every
// node reachable from a filter-allowed root via parent_id (descended
// top-down via child links). A single O(N) pass replaces what would
// otherwise be O((nodes+tabs)·depth) per-entry walks in
// nodeWorkspace / resolveTileWorkspace.
//
// The traversal does NOT skip tombstoned intermediates — a live node
// whose parent is tombstoned but whose grandparent is a registered
// root still maps to the workspace, matching the legacy walker. The
// per-entry tombstone check stays with the caller (so tombstoned
// nodes themselves are excluded from the materialized projection).
func buildNodeWorkspaceMap(state *leapmuxv1.OrgCrdtState, roots rootSet, filter SubscriberFilter) map[string]string {
	childIdx := BuildAllChildrenIndex(state)
	out := make(map[string]string, len(state.GetNodes()))
	for rootID, wsID := range roots.roots {
		if !filter.IsAllowed(wsID) {
			continue
		}
		out[rootID] = wsID
		queue := []string{rootID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, child := range childIdx[cur] {
				if _, seen := out[child]; seen {
					continue // cycle guard / multi-root collision (first root wins)
				}
				out[child] = wsID
				queue = append(queue, child)
			}
		}
	}
	return out
}

// processSubmit runs the canonical submit pipeline against the
// in-memory state. All DB writes happen inside one transaction per
// committed batch.
func (m *Manager) processSubmit(ctx context.Context, job submitJob) submitResponse {
	in := job.input
	if in.OrgID != m.orgID {
		return submitResponse{err: fmt.Errorf("manager %q received submit for org %q", m.orgID, in.OrgID)}
	}

	// Hold stateWriteMu across this submit's read-modify-write of m.state so an
	// out-of-band MutateInternal (a lifecycle create/delete on a request goroutine)
	// cannot interleave with the bare m.state map reads in ValidateBatch /
	// DiffProjectionForBatch or the commit swap below. See the stateWriteMu field.
	m.stateWriteMu.Lock()
	defer m.stateWriteMu.Unlock()

	// 1. epoch_required + stale_epoch (request-level).
	if !job.internal {
		if in.Epoch == 0 {
			return rejectAll(in.Batches, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_EPOCH_REQUIRED, "")
		}
		if in.Epoch < m.state.GetCurrentEpoch()-1 {
			return rejectAll(in.Batches, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, "")
		}
	}

	results := make([]*leapmuxv1.BatchResult, 0, len(in.Batches))
	for _, batch := range in.Batches {
		result, err := m.processBatch(ctx, in, batch)
		if err != nil {
			// A transient permission-lookup failure: return it as the whole
			// submit's error (retryable) rather than a permanent per-batch
			// rejection, so the client re-issues instead of dropping the edit.
			return submitResponse{err: err}
		}
		results = append(results, result)
	}
	return submitResponse{results: results}
}

// processBatch validates and commits a single batch. A non-nil error is a
// transient failure (e.g. a permission-lookup store error surfaced by
// ValidateBatch) that the caller propagates as a retryable Submit error rather
// than a permanent rejection, so a brief DB hiccup does not drop a user's edit.
func (m *Manager) processBatch(ctx context.Context, in SubmitInput, batch *leapmuxv1.OpBatch) (*leapmuxv1.BatchResult, error) {
	if batch.GetBatchId() == "" {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, ""), nil
	}
	if len(batch.GetOps()) == 0 {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, ""), nil
	}

	// 2. Dedup by batch_id. With per-batch rows, a retry either fully
	//    hits (same body, same principal, return cached canonical HLCs)
	//    or misses; partial-hit no longer exists.
	dedupResult, dedupRow, err := m.runDedup(ctx, in, batch)
	if err != nil {
		// A transient store error looking up the dedup row -- surface it as a
		// retryable Submit error (the same treatment ValidateBatch's res.Err
		// gets) rather than a permanent VALUE_DOMAIN rejection the client would
		// silently drop.
		return nil, err
	}
	if dedupResult != nil {
		return dedupResult, nil
	}
	if dedupRow != nil {
		// Full dedup hit — reconstruct per-op CommittedOps from the
		// stored first canonical HLC + op_count (logicals are
		// contiguous within a batch, same physical and client).
		return makeDedupHitResult(batch, dedupRow, m.state.GetCurrentEpoch()), nil
	}

	// 3. Assign canonical HLCs. One Tick per op so intra-batch LWW
	//    outcomes remain well-defined; ops share physical_ms and have
	//    contiguous logicals within a single Tick window.
	now := m.now().UnixMilli()
	for _, op := range batch.GetOps() {
		if op.GetOpId() == "" {
			op.OpId = id.Generate()
		}
		op.OriginClientId = in.OriginClient
		op.CanonicalHlc = m.clock.Tick(now)
	}

	// 4-10. Validate against working copy.
	res, working := ValidateBatch(ctx, m.state, batch.GetOps(), in.Internal, in.PrincipalID, scopedAuthChecker(m.auth, in.WorkspaceScopeID))
	if res.Err != nil {
		// A permission lookup failed transiently (store error), not a genuine
		// deny: return it so Submit surfaces a retryable error instead of a
		// permanent FORBIDDEN op-rejection that would silently drop the edit.
		return nil, res.Err
	}
	if res.Reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED {
		return rejectBatch(batch, res.Reason, res.OffendingOpID), nil
	}

	// Snapshot pre-commit state before m.commit replaces m.state
	// with `working`. The audit hook below reads worker_id from
	// the pre-tombstone tab record — applyTombstoneTab REPLACES the
	// TabRecord with a stripped {tab_type, tab_id, tombstone_at}
	// shell, so reading from `working` returns an empty worker_id.
	preState := m.state

	// 11. Commit: journal + index views + state advance, all in one tx.
	if err := m.commit(ctx, in, batch, working); err != nil {
		// A commit failure is a transient store error (the journal DB write) --
		// the batch already passed ValidateBatch, so the body-hash step inside
		// commit cannot fail here in practice. Surface it as a retryable Submit
		// error rather than a permanent VALUE_DOMAIN rejection, so a brief DB
		// hiccup does not silently drop the user's edit.
		m.logger.Error("commit batch", "err", err)
		return nil, err
	}

	// 11a. Audit: any TombstoneTabOp that removes a tab pinned to a
	// worker the principal CAN'T currently use is the orphan-cleanup
	// signal — the worker was deleted (or access was revoked) while
	// the tab was still live, and the CLI's `agent close` /
	// `terminal close` fallback walked it. The hub-side reconnect
	// sweep doesn't cover this case (a deleted worker never
	// reconnects), so this log is the only durable breadcrumb. Skip
	// under in.Internal so the legitimate WorkspaceTabsSync path
	// (worker reconnect, hub-driven tombstones) doesn't drown it
	// out.
	//
	// The audit fires in a background goroutine: m.auth.CanUseWorker
	// hits the DB (workers + worker_access_grants) and we don't want
	// to serialise every other client in the org behind that lookup.
	// preState is the pre-commit state captured above; once stored in
	// m.state's history it's immutable, so the goroutine can safely
	// read from it after the manager mutex is released. auditWG
	// drains on Stop() so log breadcrumbs always make it out.
	if !in.Internal && containsTombstoneTab(batch) {
		m.auditWG.Add(1)
		go func() {
			defer m.auditWG.Done()
			m.auditOrphanTabTombstones(in, batch, res, preState)
		}()
	}

	// 12. Per-subscriber broadcast.
	m.broadcastBatch(batch, res)

	committed := make([]*leapmuxv1.CommittedOp, 0, len(batch.GetOps()))
	for _, op := range batch.GetOps() {
		committed = append(committed, &leapmuxv1.CommittedOp{
			OpId:         op.GetOpId(),
			CanonicalHlc: HLCClone(op.GetCanonicalHlc()),
		})
	}
	maxHLC := HLCClone(batch.GetOps()[len(batch.GetOps())-1].GetCanonicalHlc())
	return &leapmuxv1.BatchResult{
		BatchId: batch.GetBatchId(),
		Outcome: &leapmuxv1.BatchResult_Committed{
			Committed: &leapmuxv1.BatchCommitted{
				Committed: committed,
				MaxHlc:    maxHLC,
				Epoch:     m.state.GetCurrentEpoch(),
			},
		},
	}, nil
}

// runDedup checks the batch's batch_id against org_recent_batch_ids.
// Outcomes:
//   - transient store error (err non-nil): the caller propagates it as a
//     retryable Submit error rather than a permanent rejection.
//   - immediate rejection (*result non-nil): returned verbatim.
//   - full hit (row non-nil, result nil): caller reconstructs the
//     original CommittedOps from the cached canonical HLC range.
//   - miss (all nil): caller proceeds with assigning canonical HLCs.
func (m *Manager) runDedup(ctx context.Context, in SubmitInput, batch *leapmuxv1.OpBatch) (*leapmuxv1.BatchResult, *RecentBatchRecord, error) {
	row, err := m.journal.LookupRecentBatchID(ctx, m.orgID, batch.GetBatchId())
	if err != nil && !errors.Is(err, ErrNotFound) {
		// A transient store failure, not a value-domain problem: surface it so
		// the caller returns a retryable Submit error instead of a permanent
		// VALUE_DOMAIN rejection the client would drop.
		return nil, nil, fmt.Errorf("lookup recent batch id: %w", err)
	}
	if row == nil {
		return nil, nil, nil
	}
	// Stored batch's epoch outside the retention window? Treat as stale.
	if row.Epoch < m.state.GetCurrentEpoch()-1 {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_STALE_EPOCH, ""), nil, nil
	}
	// Principal mismatch: reject.
	if row.PrincipalID != in.PrincipalID {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_OP_ID_COLLISION_UNAUTHORIZED, ""), nil, nil
	}
	// op_count mismatch: reject (would prevent canonical HLC
	// reconstruction even if the body somehow matched).
	if row.OpCount != int64(len(batch.GetOps())) {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_OP_ID_COLLISION, ""), nil, nil
	}
	// Body mismatch: reject. A hashing failure here is a deterministic
	// value-domain problem (a malformed body that would also fail in commit),
	// not a transient store error, so it stays a permanent rejection.
	bodyHash, err := BatchBodyHash(batch)
	if err != nil {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, ""), nil, nil
	}
	if !bytes.Equal(bodyHash, row.BodyHash) {
		return rejectBatch(batch, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_OP_ID_COLLISION, ""), nil, nil
	}
	return nil, row, nil
}

// commit performs the per-batch transactional write. The journal owns
// the DB transaction boundary; on rollback the manager's in-memory
// state stays at `m.state` and the canonical HLCs minted for this batch
// are simply discarded (they're strictly greater than any future tick,
// so no client can observe them).
func (m *Manager) commit(ctx context.Context, in SubmitInput, batch *leapmuxv1.OpBatch, working *leapmuxv1.OrgCrdtState) error {
	// DiffProjectionForBatch skips the per-tab chain walks for tabs the
	// batch cannot possibly transition. Non-structural batches (the
	// common case: user-triggered tab open/move/close) re-project only
	// the tabs they explicitly name; structural batches fall back to
	// full Project + Diff. The latter is at most as expensive as the
	// pre-existing path (two ProjectOwnership calls), so this is a
	// strict improvement.
	diff := DiffProjectionForBatch(m.state, working, batch.GetOps())

	hash, err := BatchBodyHash(batch)
	if err != nil {
		return fmt.Errorf("hash batch body: %w", err)
	}
	dedupRow := RecentBatchRecord{
		OrgID:             m.orgID,
		BatchID:           batch.GetBatchId(),
		BodyHash:          hash,
		PrincipalID:       in.PrincipalID,
		CanonicalFirstHLC: HLCClone(batch.GetOps()[0].GetCanonicalHlc()),
		OpCount:           int64(len(batch.GetOps())),
		Epoch:             m.state.GetCurrentEpoch(),
		ExpiresAt:         m.now().Add(DedupTTL),
	}

	if err := m.journal.CommitBatch(ctx, CommitBatch{
		OrgID:       m.orgID,
		Batch:       batch,
		PrincipalID: in.PrincipalID,
		Epoch:       m.state.GetCurrentEpoch(),
		DedupRow:    dedupRow,
		IndexDiff:   diff,
	}); err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}

	m.mu.Lock()
	m.state = working
	m.mu.Unlock()
	return nil
}

// State returns a deep clone of the current state. Used by tests +
// callers that need to retain the state past the manager's RLock
// window (e.g. send it across a goroutine boundary). Hot-path readers
// that just walk the state during one synchronous pass should prefer
// WithStateRLock to avoid the per-call clone.
func (m *Manager) State() *leapmuxv1.OrgCrdtState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return CloneState(m.state)
}

// WithStateRLock runs `fn` against the live in-memory state under
// m.mu.RLock so the caller avoids a multi-MB CloneState allocation when
// it only needs a synchronous walk (enumeration, projection,
// computation). The state pointer MUST NOT escape `fn` — callers that
// need to hold the state past the call should use State() instead.
func (m *Manager) WithStateRLock(fn func(state *leapmuxv1.OrgCrdtState)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fn(m.state)
}

// LocateTileWorkspace walks the live state from `tileID` up through
// parent_id links and returns the workspace_id of the matching root,
// or "" when the chain doesn't terminate at a registered workspace
// root. Runs under m.mu.RLock so the lookup is a single synchronous
// pass — callers don't pay a full-state clone per RPC.
func (m *Manager) LocateTileWorkspace(tileID string) string {
	if tileID == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state == nil {
		return ""
	}
	return FindRootWorkspace(m.state.GetNodes(), m.state.GetWorkspaces(), tileID)
}

// currentEpoch returns the current epoch under the manager RLock.
// Use this instead of `m.state.GetCurrentEpoch()` from goroutines that
// don't own `m.mu` (e.g. lifecycle-outbox consumers running on
// workspace_service request-handler goroutines) — bare reads on
// `m.state` race with the manager goroutine's writes under
// `m.mu.Lock()` in `commit`.
func (m *Manager) currentEpoch() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.GetCurrentEpoch()
}

// MutateInternal lets the lifecycle outbox consumer mutate manager-internal
// state (workspaces map, current_epoch) from a request goroutine. It takes
// stateWriteMu — the same lock processSubmit / maybeAdvanceEpoch hold across the
// manager goroutine's read-modify-write of m.state — so an in-place write here
// cannot race the manager goroutine's bare m.state reads (a concurrent map
// read+write) nor be clobbered by the commit swap. m.mu still excludes the RLock
// readers (Materialized, currentEpoch, broadcast) while the maps change.
//
// Do NOT call this from the manager goroutine itself (inside processSubmit /
// maybeAdvanceEpoch): those already hold stateWriteMu, so re-taking it would
// deadlock. maybeCompact -- the other tickHousekeeping arm -- does NOT hold
// stateWriteMu, but it never races this method: it touches m.state only under
// m.mu (RLock to clone, Lock to advance the monotonic watermark / prune
// tombstones), and runs on the same single Start-loop goroutine as
// processSubmit, so it cannot overlap the manager goroutine's bare m.state
// reads. The lifecycle-outbox consumer runs on the request goroutine, which is
// the intended caller.
func (m *Manager) MutateInternal(fn func(state *leapmuxv1.OrgCrdtState)) {
	m.stateWriteMu.Lock()
	defer m.stateWriteMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.state)
}

func rejectAll(batches []*leapmuxv1.OpBatch, reason leapmuxv1.BatchRejectionReason, opID string) submitResponse {
	results := make([]*leapmuxv1.BatchResult, len(batches))
	for i, b := range batches {
		results[i] = rejectBatch(b, reason, opID)
	}
	return submitResponse{results: results}
}

func rejectBatch(batch *leapmuxv1.OpBatch, reason leapmuxv1.BatchRejectionReason, opID string) *leapmuxv1.BatchResult {
	return &leapmuxv1.BatchResult{
		BatchId: batch.GetBatchId(),
		Outcome: &leapmuxv1.BatchResult_Rejected{
			Rejected: &leapmuxv1.BatchRejection{Reason: reason, OffendingOpId: opID},
		},
	}
}

// ErrNotFound mirrors store.ErrNotFound for crdt-package consumers
// that don't import store directly.
var ErrNotFound = errors.New("crdt: not found")
