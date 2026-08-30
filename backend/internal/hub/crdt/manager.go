package crdt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/util/panicsafe"
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
	// DedupTTL is how long a batch_id stays in user_recent_batch_ids.
	DedupTTL = 14 * 24 * time.Hour
	// OpRetentionTTL is how far behind max_hlc the op_retention_watermark
	// lags: op batches whose last canonical HLC is older than this (relative
	// to the live max_hlc) may be deleted by maybeCompact, while tombstones
	// are pruned at the much tighter compaction_watermark (= max_hlc). The
	// two diverge because tombstone pruning is load-bearing for correctness
	// (HLC monotonicity, bounded user_state growth), while op-batch deletion
	// is pure storage optimization — the op log is an append-only history
	// and replaying a longer tail is sound (each op is idempotent under the
	// CRDT merge). The retention window is what widens delta-resume beyond a
	// ~60s reconnect gap to multi-hour gaps (a client refresh after a few
	// hours still resumes instead of taking a full snapshot), so long as the
	// post-cursor tail still fits MaxResumeDeltaOps / MaxResumeDeltaBytes —
	// those budget caps are independent and still force a FALLBACK when the
	// tail is too large to ship as a delta. 24h is a conservative first
	// window; pick it against measured per-user op volume before widening.
	OpRetentionTTL = 24 * time.Hour
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
	// MaxResumeDeltaOps bounds the op-count budget a SubscribeWithACL replay may
	// ship as a delta before falling back to a full materialized snapshot. A
	// reconnecting client whose last-applied HLC is far enough behind that the
	// post-cursor journal tail exceeds this many ops gets a full `initial`
	// frame instead -- the delta would be larger than the snapshot it exists
	// to avoid. Picked well above a single busy session's churn but below the
	// point where marshaling + writing the tail costs more than materialized.
	MaxResumeDeltaOps = 5000
	// MaxResumeDeltaBytes bounds the decoded journal payload a resume may ship
	// (sum of batch_payload + transitions_payload over the accepted tail).
	// Op count alone under-budgets after per-batch record snapshots landed in
	// transitions_payload: a few thousand ops can still exceed the client WS
	// read limit (UserEventsReadLimit / desktop maxFrameSize). Cap well under
	// those frame ceilings so an oversized tail falls back to `initial`
	// instead of reconnect-looping on a frame the client cannot read.
	//
	// MEASURED ON SOURCE ROWS, NOT ON THE EMITTED FRAME, so it is approximate
	// with respect to what actually goes on the wire -- and approximate in the
	// permissive direction: buildResumeDelta ADDS to the rows it reads (one
	// BatchEnd per tail batch, synthesized EntityRemoved frames, a fresh AtHlc
	// stamp on each EntityMaterialized, and a WatchUserEvent envelope per
	// frame). The budget is checked during a streaming, paged scan, before any
	// delta exists to measure, which is also what makes it the right place to
	// bound MEMORY. Nothing here bounds that gap: op count does NOT bound the
	// transition frames, because a single SetFloatingWindowRegister(workspace_id)
	// drags a whole subtree into AffectedEntities, so transition entries per batch
	// are themselves bounded by this byte budget rather than by
	// MaxResumeDeltaOps. What the wire actually carries is therefore gated
	// separately and exactly, by MaxResumeDeltaFrameBytes below: buildResumeDelta
	// runs proto.Size on the BUILT delta as an ADDITIONAL check after this one, so
	// raising either ceiling cannot silently produce an unreadable frame.
	MaxResumeDeltaBytes = 4 << 20 // 4 MiB
	// resumeDeltaEnvelopeHeadroom is what the wire frame adds on top of the
	// ResumeDelta MaxResumeDeltaFrameBytes measures: the WatchUserEvent oneof
	// wrapper, the 4-byte length prefix channelwire.WriteFramedBytes prepends,
	// and the subscriber_client_id + user_id SubscribeOutcome.Bootstrap stamps
	// onto the delta AFTER buildResumeDelta has sized it. Real overhead is a few
	// tags, two varint lengths and two ids -- a few hundred bytes. 64 KiB is
	// deliberate slack (the same figure, for the same reason, as
	// contracts.InnerEnvelopeHeadroom) so that adding a field to ResumeDelta or
	// to the bootstrap stamping cannot quietly push a passing delta over the
	// read limit.
	resumeDeltaEnvelopeHeadroom = 64 << 10 // 64 KiB
	// MaxResumeDeltaFrameBytes is the ceiling on the BUILT ResumeDelta, checked
	// with proto.Size in buildResumeDelta once the frame stream is assembled.
	//
	// Derived from channelwire.UserEventsReadLimit rather than written as its own
	// number: that limit is what the subscriber's socket enforces
	// (wsConn.SetReadLimit in ws_userevents.go, and the same value on every Go
	// consumer via OpenUserEventsWS), and the desktop sidecar's frame ceiling is
	// that plus 4 MiB, so the read limit is the binding one. A delta past it does
	// not degrade -- the whole delta is ONE WebSocket message, so the read fails,
	// the client reconnects with the same cursor, rebuilds the same oversized
	// frame, and loops with no server-side error. Deriving keeps the gate moving
	// with the limit it protects instead of drifting from it.
	MaxResumeDeltaFrameBytes = channelwire.UserEventsReadLimit - resumeDeltaEnvelopeHeadroom
)

// OpRetentionCutoffPhysicalMs is the HLC physical below which an op batch is
// eligible for deletion, given wall-clock time now and a retention window ttl.
//
// This is the ONE definition of the retention floor. The cross-user sweep
// (store.CleanupStore.DeleteUserOpBatchesBeforePhysical) deletes rows below
// it, and Manager.decideResume refuses a resume cursor below it, so the set of
// rows the sweep may remove and the set of cursors a resume may accept cannot
// disagree.
//
// It returns an HLC physical rather than a wall clock because the cursor lives
// in that domain. HLC physical is Unix epoch-ms, but it is NOT the local wall
// clock: Clock.Tick clamps it monotonically and Clock.Observe re-seeds it from
// persisted state, so it can run permanently ahead of real time after a
// backward clock correction. Deriving the cutoff FROM now() and comparing it
// AGAINST HLC physicals is the deliberate arrangement -- the floor advances
// with real time (so dormant accounts drain) while both sides read one number.
// Sweeping the committed_at column instead would reintroduce the split, and
// worse, committed_at is stamped by the DB server, a different machine under
// Postgres and MySQL.
func OpRetentionCutoffPhysicalMs(now time.Time, ttl time.Duration) int64 {
	return now.Add(-ttl).UnixMilli()
}

// SubmitInput is what callers hand the manager. internal=true skips
// per-op auth and is required for SetWorkspaceRootNodeOp.
//
// It names no tenant: the manager it is submitted to IS the tenant. Registry.Get
// keys managers by user id and refuses a blank one, and the factory builds
// NewManager from that same key -- so a UserID field here could only restate
// what the receiver already knows, and its sole consumer was the gate that
// compared it back against m.owner.String(). Dropping it makes "a submit landed on the
// wrong tenant's manager" unrepresentable rather than merely detected.
//
// PrincipalID is the other axis and stays: it names WHO is writing (a session,
// a delegation bearer, or the hub), which nothing about the receiver supplies.
type SubmitInput struct {
	Epoch        int64
	Batches      []*leapmuxv1.OpBatch
	PrincipalID  string
	OriginClient string
	// WorkerScope, when non-nil, narrows which WORKER ids this principal's ops
	// may reference to those the delegation token's minting worker is entitled to
	// reach. It is the ONLY narrowing a delegation bearer carries: it bounds
	// WHICH MACHINE the bearer may bind a tab to, which is the one reach
	// authenticating as the tab's own user does not already grant.
	//
	// It is a predicate rather than an id because the rule (target IS the minter,
	// or the token's user owns the minter) lives in the auth package, which this
	// package deliberately does not import -- the same reason AuthChecker is an
	// interface here. nil means unbounded, which is what a session or API
	// credential gets.
	//
	// Without it a leaked token minted by worker A carrying its user's identity
	// can point a tab at ANY worker that user owns through SetTabRegisterOp,
	// because CanUseWorker only asks "may this USER use this worker". That is
	// the same cross-machine reach service.WorkerReachAuthorizer refuses one
	// layer over.
	WorkerScope func(workerID string) bool
	Internal    bool
}

// Manager owns one user's CRDT state and its journal, and coordinates
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
	owner       userid.UserID
	clock       *Clock
	hubClientID string

	mu    sync.RWMutex
	state *leapmuxv1.UserCrdtState
	// The manager goroutine (Start's select loop) is the SOLE writer of
	// m.state: every mutation -- including workspace lifecycle create/delete,
	// which flows through SubmitInternal as SetWorkspaceRegisterOp /
	// TombstoneWorkspaceOp -- goes through processSubmit / maybeAdvanceEpoch on
	// that one goroutine. That makes the bare m.state map reads in
	// ValidateBatch / DiffProjectionForBatch legitimately lock-free.
	// m.mu (RWMutex) guards m.state only against the RLock readers
	// (Materialized, currentEpoch, broadcast, WithStateRLock) and the commit
	// swap (m.state = working). The lone non-production writer is
	// SeedStateForTest, which also runs on the manager goroutine (via taskCh)
	// so it cannot race those bare reads; see its doc.
	subscribers *SubscriberController
	projection  sync.Mutex
	// subscribeExpandMu serializes SubscribeWithACL's resolve+register against
	// ExpandSubscribersForWorkspace's read-ACL-then-apply (workspace create).
	// Without it, a subscriber that resolved its filter BEFORE a concurrent
	// CreateWorkspace committed, but registered AFTER that workspace's expand
	// pass ran, would permanently miss the workspace until reconnect (the
	// expand only visits already-registered subscribers). It is taken OUTSIDE
	// projection (subscribeExpandMu -> projection -> m.mu) and never held
	// across a DB commit's op-broadcast-blocking window.
	subscribeExpandMu sync.Mutex
	// lifecycleMu serializes SubmitLifecycle drains against each other. Every
	// workspace lifecycle RPC drains the outbox post-commit on its own request
	// goroutine, so without this two mutations for the same user drain
	// concurrently -- and the per-row apply logic is written against a single
	// sequential consumer (contractSubscribersForWorkspace's single-key-delete
	// safety argument, applyLifecycleCreate's filter expand + seed batch, and
	// the fixed "lifecycle-<op>-<ws>" batch ids all assume a create and a
	// delete of the same workspace are never in flight at once). It is held
	// across ListPending too, so a drain that starts while another is in
	// flight observes the first drain's consume-marks instead of re-listing
	// and re-applying its rows. Lock order: lifecycleMu is outermost of the
	// lifecycle path (lifecycleMu -> subscribeExpandMu -> projection -> m.mu);
	// nothing acquires lifecycleMu while holding another manager lock.
	lifecycleMu sync.Mutex
	// undecodableLogged tracks lifecycle_outbox row IDs whose decode failure has
	// already been logged at Error, so a row whose MarkLifecycleOutboxConsumed
	// keeps failing transiently is not re-logged on every subsequent drain. The
	// commit's contract is one Error log per corrupt row; without this a corrupt
	// row whose consume races a long-lived DB write fault would re-decode,
	// re-fail, and re-log on every lifecycle RPC for the user for as long as the
	// fault lasts. Guarded by lifecycleMu (every access is inside SubmitLifecycle).
	// Entries are removed once the row is successfully consumed, so the set is
	// bounded by the number of currently-stuck corrupt rows.
	undecodableLogged map[int64]bool
	// applyFailedLogged tracks lifecycle_outbox row IDs whose applyLifecycleRow
	// failure has already been logged at Error. A row that fails to apply is NOT
	// consumed -- it retries on the next drain, since an apply failure is most
	// often transient (a create whose read-ACL lookup faulted) -- so without this
	// dedup a persistently-failing row re-logs Error on every lifecycle RPC for
	// the user for as long as the fault lasts, exactly the amplification
	// undecodableLogged exists to prevent for decode failures. The row is still
	// retried (and re-pay the store lookup); only the log is deduped. Mirrors
	// undecodableLogged: one Error then Warn, cleared once the row succeeds so
	// the set stays bounded by the number of currently-stuck rows. Guarded by
	// lifecycleMu.
	applyFailedLogged map[int64]bool
	presenceCtl       *PresenceController
	auth              AuthChecker
	journal           Journal
	now               func() time.Time
	logger            *slog.Logger

	// clearGrace seeds presenceCtl at construction. Held on Manager
	// (rather than only inside the controller) so WithPresenceClearGrace
	// can mutate it before NewManager finishes building the controller.
	clearGrace time.Duration

	// opRetentionTTL seeds the lag between compaction_watermark and
	// op_retention_watermark in maybeCompact. Held on Manager so
	// WithOpRetentionTTL can mutate it before Start, and so tests can shrink
	// it to milliseconds to assert the retention/drop boundary without
	// sleeping through the production 24h default.
	opRetentionTTL time.Duration

	// maxResumeDeltaFrameBytes is the ceiling buildResumeDelta enforces on the
	// BUILT delta (proto.Size). Held on Manager rather than read from the
	// constant so WithMaxResumeDeltaFrameBytes can shrink it in tests, which is
	// the only way to exercise the FALLBACK without materializing a ~16 MiB
	// fixture.
	maxResumeDeltaFrameBytes int

	// materialize builds the FALLBACK baseline. Always materializedFromState in
	// production; WithMaterializerForTest swaps it so a test can hold a baseline
	// mid-flight and assert nothing is stalled behind it. Set in NewManager, so
	// it is never nil.
	materialize BaselineBuilder

	// reconcileNudger, when set, is told which workers hosted a tab a batch
	// just tombstoned, so each can converge immediately instead of waiting out
	// its reconciler interval. Nil disables the nudge.
	reconcileNudger ReconcileNudger

	submitCh   chan submitJob
	internalCh chan submitJob
	// taskCh carries caller-driven work (SeedStateForTest's seed closure,
	// TickHousekeeping's forced pass) to the manager goroutine, so both run on
	// the sole writer (see managerTask). Both PUBLISH new state generations --
	// the seed's clone, compaction's pruned clone, the epoch bump -- and a
	// publish from any other goroutine would clobber a commit that landed after
	// the clone was taken. Unbuffered: the caller blocks until the goroutine
	// services the task, giving it a happens-before edge against anything it
	// does next, including a later submit's bare reads.
	taskCh chan managerTask
	// startedOnce closes startedChan exactly once, the first time Start runs,
	// BEFORE the select loop begins servicing jobs. startedChan is the readiness
	// signal the registry waits on before handing the manager out, so any caller
	// that observes the manager after Get sees started() as closed and routes its
	// seed through the goroutine (the sole writer) rather than the pre-Start
	// direct-write branch -- closing the race the old atomic.Bool left open (Get
	// spawned `go Start()` and returned before Start had set the flag, so a
	// post-Get SeedStateForTest could read started==false and write m.state on
	// the caller's goroutine while the manager goroutine was already doing bare
	// reads in ValidateBatch).
	startedOnce sync.Once
	startedChan chan struct{}
	stop        chan struct{}
	done        chan struct{}
	// stopOnce makes Stop safe to call concurrently: two callers (e.g. the
	// registry's Shutdown and a test, or SweepIdle racing a manual Stop) would
	// otherwise both pass the select-default arm and both close(m.stop), the
	// second panicking with 'close of closed channel'. Once guarantees the
	// close lands exactly once; both callers still wait on <-m.done and run
	// the idempotent teardown (PresenceController.Shutdown and auditWG.Wait).
	stopOnce sync.Once

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

	// nudgeWG tracks the background reconcile-nudge goroutines. Stop()
	// deliberately does NOT wait on it: a nudge is best-effort -- losing one on
	// shutdown costs only the delay until the worker's next reconcile tick --
	// so draining it would give shutdown one more thing to wait on and salvage
	// nothing. Only WaitForNudges (tests) waits, which is safe because a test
	// nudger returns promptly.
	nudgeWG sync.WaitGroup
}

// overflowed reports whether the transport flagged a dropped frame. A nil
// callback (every non-websocket caller, and every test that does not care)
// reads as "no overflow".
func (s *Subscriber) overflowed() bool {
	return s.Overflowed != nil && s.Overflowed()
}

// SubscriberFilter narrows the events a subscriber receives.
//
// The WorkspaceIDs map is treated as immutable after the filter value is
// installed on a Subscriber: expand/contract REPLACE the whole Filter via
// WithWorkspace / WithoutWorkspace rather than mutating the map in place.
// That makes a plain value copy of SubscriberFilter safe to hand to a
// long-running resume scan without snapshotFilter — no concurrent map
// read/write against lifecycle expand/contract.
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

// WithWorkspace returns a filter that also allows workspaceID. The receiver
// is never mutated: a new map is allocated when the key is absent. A nil
// (allow-all) filter is already allowed for every id, so it is returned
// unchanged. Callers MUST assign the result (sub.Filter = sub.Filter.WithWorkspace(id)).
func (f SubscriberFilter) WithWorkspace(workspaceID string) SubscriberFilter {
	if workspaceID == "" || f.IsAllowed(workspaceID) {
		return f
	}
	// f.WorkspaceIDs is non-nil here (nil would have IsAllowed==true).
	next := make(map[string]bool, len(f.WorkspaceIDs)+1)
	for ws, allowed := range f.WorkspaceIDs {
		next[ws] = allowed
	}
	next[workspaceID] = true
	return SubscriberFilter{WorkspaceIDs: next}
}

// WithoutWorkspace returns a filter that no longer allows workspaceID. The
// receiver is never mutated. A nil (allow-all) filter is returned unchanged
// (contractors only run on concrete ACL maps). Callers MUST assign the result.
func (f SubscriberFilter) WithoutWorkspace(workspaceID string) SubscriberFilter {
	if f.WorkspaceIDs == nil {
		return f
	}
	if _, ok := f.WorkspaceIDs[workspaceID]; !ok {
		return f
	}
	next := make(map[string]bool, len(f.WorkspaceIDs))
	for ws, allowed := range f.WorkspaceIDs {
		if ws == workspaceID {
			continue
		}
		next[ws] = allowed
	}
	return SubscriberFilter{WorkspaceIDs: next}
}

// NewSubscriberFilter copies ids into a private map so the caller cannot
// mutate the filter through the input map. nil means allow-all.
func NewSubscriberFilter(ids map[string]bool) SubscriberFilter {
	if ids == nil {
		return SubscriberFilter{}
	}
	out := make(map[string]bool, len(ids))
	for ws, allowed := range ids {
		out[ws] = allowed
	}
	return SubscriberFilter{WorkspaceIDs: out}
}

// Subscriber is a single open user-event subscription (one connected
// `/ws/userevents` client, or a one-shot in-process test reader).
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
	Filter                SubscriberFilter
	// resumeSuppressThrough, when non-nil, is the register-time MaxHlc
	// high-water for this subscription. Live batch broadcasts whose last-op HLC
	// is <= this value are skipped for this subscriber, because they are
	// already covered by whatever bootstrap frame the register window chose:
	//
	//   - RESUME: the ResumeDelta's journal scan ships everything in
	//     (cursor, until], so broadcasting those batches too would
	//     dual-deliver the same catch-up frames.
	//   - FALLBACK: the baseline is built from the generation whose max_hlc
	//     THIS value is, so every batch at or below it is already folded into
	//     the snapshot. Re-delivering one would replay an entity_materialized /
	//     entity_removed the client applies WHOLESALE onto a newer baseline.
	//
	// So the gate is owned by whichever bootstrap established the baseline, not
	// by the resume path specifically. Set atomically with registration under
	// m.projection (see registerForResume / registerForFallback); left set for
	// the life of the connection (later commits always have a strictly greater
	// HLC). Presence / lifecycle frames bypass it (they go through broadcastTo,
	// not sendTo).
	resumeSuppressThrough *leapmuxv1.HLC
	// Overflowed, when set, reports that the transport could not buffer a frame
	// while the bootstrap frame was still being built -- a resume's journal scan
	// or a fallback's baseline walk, both of which run registered and unlocked.
	//
	// The transport used to answer that by tearing the connection down, which
	// sent the client back with the SAME cursor to rebuild the SAME multi-page
	// scan -- under the sustained broadcast load that caused the overflow, so it
	// could overflow again. The loop is bounded (the widening gap eventually
	// trips MaxResumeDeltaOps and FALLBACKs anyway), but every round is a wasted
	// multi-page journal scan on a hub already under the load that triggered it.
	// buildResumeDelta consults this instead and converts the overflow into ONE
	// snapshot; fallbackOutcome consults it to retry its baseline at a newer
	// generation (see the park-overflow ladder).
	//
	// A CALLBACK, like OnRebaseline, rather than a flag on this struct: the
	// state belongs to the transport's queue, and Subscriber is copied by value
	// (see cloneSubscriber), which no synchronisation primitive survives.
	Overflowed func() bool
	// OnRebaseline, when set, is called at the moment a registration is REPLACED
	// by a fresh one -- a RESUME that gave up, or a FALLBACK
	// retrying after its park buffer overflowed -- while m.projection is held
	// and BEFORE the new baseline's generation is captured.
	//
	// It exists because the transport buffers frames it has not written yet.
	// While a bootstrap frame is being built the subscriber is registered, so
	// live broadcasts enqueue normally; if that attempt is then abandoned, the
	// replacement baseline is taken at a LATER point than those queued frames.
	// Writing them after it would apply older entity records on top of newer
	// ones -- and unlike batch ops, entity_materialized / entity_removed are
	// applied wholesale by the client with no HLC compare, so nothing corrects
	// it afterwards.
	//
	// Called under m.projection precisely so no broadcast can interleave: every
	// frame queued before this point is superseded by the new baseline, and
	// every frame after it is newer. Implementations must not block or re-enter
	// the manager, and must also clear whatever Overflowed reports -- the frames
	// that flag refers to are exactly the ones just discarded.
	OnRebaseline func()
	// Send delivers one event to this subscriber.
	//
	// Contract: Send MUST return promptly — implementations either push
	// to a bounded per-subscriber buffer with a non-blocking select
	// (returning ErrSubscriberSlow when full so the subscriber can be
	// torn down) or do the work synchronously. Send MUST NOT block on
	// network IO or a full unbounded buffer; the manager's broadcast
	// goroutine fans out to every subscriber sequentially, so one slow
	// Send would head-of-line block the entire user's broadcasts.
	//
	// Manager dedupes the *MarshaledEvent pointer across subscribers
	// that receive the same underlying *leapmuxv1.WatchUserEvent, so
	// callers that marshal the payload (e.g. ws_userevents.go) can call
	// `evt.Bytes()` and pay the proto.Marshal cost once per broadcast —
	// not once per subscriber.
	Send func(*MarshaledEvent) error
}

func subscriberMaySeeWorkspace(sub *Subscriber, workspaceID string) bool {
	return sub.RequestedWorkspaceIDs == nil || sub.RequestedWorkspaceIDs[workspaceID]
}

// ErrSubscriberSlow signals that a subscriber's bounded send buffer is
// full and the subscriber should be torn down rather than waited on.
// Callers' Send implementations return this when their internal queue
// can't accept another event without blocking; the WS handler treats
// it as a fatal signal that cancels the per-subscriber context and
// drops the connection.
var ErrSubscriberSlow = errors.New("crdt: subscriber send buffer full")

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

// managerTask carries caller-driven work onto the manager goroutine, so it runs
// on the sole writer exactly as the loop's own arms do. Running there is what
// makes a write mechanically single-writer: it cannot race the goroutine's bare
// m.state reads in ValidateBatch / DiffProjectionForBatch, and — since both
// current users PUBLISH a new generation — a publish from the caller's
// goroutine would silently discard any commit that landed between the clone and
// the swap.
//
// One task type rather than one per caller (a seedJob and a housekeepJob were
// byte-for-byte the same handshake) so the subtle part — the started/stopped/
// pre-Start negotiation in runOnManagerGoroutine — has exactly one home. Each
// task closes over whatever it needs, including the caller's ctx. done is
// closed once run has returned.
type managerTask struct {
	run  func()
	done chan struct{}
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

// ReconcileNudger asks a worker to run its orphan reconciler now.
//
// A narrow interface, injected, for the same reason AuthChecker is: the CRDT
// manager must not depend on the worker-connection manager, and a test drives it
// with a recorder rather than a live bidi stream.
type ReconcileNudger interface {
	// NudgeReconcile is best-effort and non-blocking. A worker that is offline,
	// or whose stream is busy, simply does not get nudged -- its reconciler still
	// converges on its next tick or reconnect, which is what the nudge
	// accelerates rather than replaces.
	//
	// Takes no principal, deliberately. The nudge is a server-initiated flow, and
	// carrying a user id through it would make the worker id look user-supplied to
	// a reader (and to the repo's worker-reach invariant, which is right to insist
	// on that). The reconciler re-reads its own inventory, so the worker id is the
	// only thing the nudge needs.
	NudgeReconcile(workerID string)
}

// WithReconcileNudger wires the convergence nudge sent when a batch tombstones a
// tab. Left unset, tab closes converge on the reconciler's own schedule.
func WithReconcileNudger(n ReconcileNudger) ManagerOption {
	return func(m *Manager) { m.reconcileNudger = n }
}

// WithPresenceClearGrace overrides the deferred presence-clear grace
// window. Tests use this to keep the grace short (tens of ms) so they
// don't have to sleep through the production default
// (`PresenceClearGrace`, 60 s).
func WithPresenceClearGrace(d time.Duration) ManagerOption {
	return func(m *Manager) { m.clearGrace = d }
}

// WithOpRetentionTTL overrides the lag between compaction_watermark and
// op_retention_watermark (the floor below which op batches may be deleted).
// Tests use this to shrink the retention window to milliseconds so they can
// assert the drop/retain boundary and the resume gate without sleeping through
// the production 24h default (`OpRetentionTTL`).
//
// TEST-ONLY, and deliberately not wired to production config: it shifts THIS
// manager's compaction lag and resume gate, but the cross-user retention sweep
// in the cleanup job reads the `OpRetentionTTL` constant directly and has no
// per-manager reach (a dormant user has no resident manager at all -- that is
// the case the sweep exists for). Widening the window here without widening
// the constant would admit cursors whose rows the sweep has already deleted.
func WithOpRetentionTTL(d time.Duration) ManagerOption {
	return func(m *Manager) { m.opRetentionTTL = d }
}

// WithMaxResumeDeltaFrameBytes overrides the ceiling buildResumeDelta enforces
// on the BUILT ResumeDelta (`MaxResumeDeltaFrameBytes`).
//
// TEST-ONLY, and deliberately not wired to production config: the production
// value is DERIVED from channelwire.UserEventsReadLimit, the limit every
// subscriber's socket actually enforces, so an operator-tunable copy could only
// disagree with the socket it exists to protect. Tests shrink it to a few
// hundred bytes to assert the over-ceiling FALLBACK without building a ~16 MiB
// fixture.
func WithMaxResumeDeltaFrameBytes(n int) ManagerOption {
	return func(m *Manager) { m.maxResumeDeltaFrameBytes = n }
}

// BaselineBuilder builds a FALLBACK subscriber's full snapshot from a captured
// state generation. Named only so WithMaterializerForTest can be spelled as a
// decorator.
type BaselineBuilder func(state *leapmuxv1.UserCrdtState, filter SubscriberFilter) *leapmuxv1.UserMaterialized

// WithMaterializerForTest DECORATES the FALLBACK baseline builder.
//
// TEST-ONLY. It exists because the property the FALLBACK rework is FOR --
// "building an O(all-entities) baseline no longer stalls commits, broadcasts,
// expands or Materialized()" -- can only be asserted while a baseline is
// actually in flight, and the real builder offers nothing to block on. The
// RESUME arm's equivalent test blocks inside the fake journal's
// ListBatchesAfter; there is no journal call on this path, so the seam has to
// be the builder itself.
//
// A DECORATOR rather than a replacement: `decorate` receives the real builder
// and is expected to call it, so the test blocks around production behaviour
// instead of standing in for it. That also keeps materializedFromState
// unexported.
func WithMaterializerForTest(decorate func(next BaselineBuilder) BaselineBuilder) ManagerOption {
	return func(m *Manager) { m.materialize = decorate(m.materialize) }
}

// truncateRunes returns the first n runes of s, never splitting one.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// NewManager constructs a manager. Callers MUST call Bootstrap before
// Start so the in-memory state is consistent with disk.
//
// The lifecycle outbox reader is passed per-call to SubmitLifecycle
// instead of being stashed on the manager — the manager only ever
// reads from one when the service-layer drains pending rows, and
// holding a reference here served no purpose.
func NewManager(owner userid.UserID, journal Journal, auth AuthChecker, logger *slog.Logger, now func() time.Time, opts ...ManagerOption) *Manager {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	// Truncate by RUNE, not by byte. userID is an ASCII nanoid today, but
	// nothing on the way here enforces that -- userid.New only refuses the
	// empty string -- and a byte slice through a multi-byte rune would make
	// hubClientID invalid UTF-8. That is not merely cosmetic: it rides out on
	// every hub-originated op as origin_client_id, a proto3 string, and Go's
	// protobuf runtime REFUSES to marshal invalid UTF-8 -- so it would fail
	// every hub-internal commit for that user rather than just looking wrong.
	hubID := HubReservedPrincipal + "-" + truncateRunes(owner.String(), 8)
	m := &Manager{
		owner:                    owner,
		clock:                    NewClock("hub-canonical"),
		hubClientID:              hubID,
		auth:                     auth,
		journal:                  journal,
		now:                      now,
		logger:                   logger.With("user_id", owner.String()),
		clearGrace:               PresenceClearGrace,
		opRetentionTTL:           OpRetentionTTL,
		maxResumeDeltaFrameBytes: MaxResumeDeltaFrameBytes,
		materialize:              materializedFromState,
		submitCh:                 make(chan submitJob, 64),
		internalCh:               make(chan submitJob, 16),
		taskCh:                   make(chan managerTask),
		startedChan:              make(chan struct{}),
		stop:                     make(chan struct{}),
		done:                     make(chan struct{}),
		subscribers:              newSubscriberController(),
	}
	for _, opt := range opts {
		opt(m)
	}
	m.presenceCtl = newPresenceController(now, m.clearGrace, m.stop)
	return m
}

// Bootstrap loads user_state + replays user_op_batches > watermark. Safe
// to call before Start.
func (m *Manager) Bootstrap(ctx context.Context) error {
	state, tail, err := m.journal.LoadState(ctx, m.owner.String())
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state == nil {
		state = NewState(m.owner.String())
		state.EpochStartedAt = nil
	} else if err := m.requireOwnState(state); err != nil {
		return err
	}
	ensureStateMaps(state)
	for _, batch := range tail {
		for _, op := range batch.GetOps() {
			Apply(state, op)
		}
	}
	m.clock.Observe(state.GetMaxHlc())
	m.commitState(state)
	// Stamp activity so a freshly-bootstrapped manager isn't eligible
	// for immediate eviction. The registry's idle-eviction window
	// applies from the bootstrap moment onward; without this, a manager
	// loaded at t=0 with no traffic would look eternally idle (lastActivity
	// zero-value) and the janitor would tear it down on the next sweep.
	m.touchActivity()
	return nil
}

// errBlankManagerTenant guards the one hole the typed field cannot close:
// userid.UserID's zero value is constructible, so NewManager(userid.UserID{})
// still compiles. Registry.Get is the only production constructor and mints
// before calling, so this is the in-process/test backstop.
var errBlankManagerTenant = errors.New("crdt: manager has a blank user id; it was not created through Registry.Get")

// requireOwnState refuses a loaded state payload that names a tenant other than
// this manager's.
//
// The manager's tenancy is m.owner.String(): the key LoadState fetched by, the key
// Registry.Get built it from, and the key every batch it commits is written
// under.
// The `user_id` INSIDE the payload is data, and adopting it silently would make
// the blob the authority over the key -- with consequences well outside the
// CRDT, because CompactBatch keys the next user_state row by
// state.GetUserId(). A payload naming another tenant would rewrite that
// tenant's state row; a payload naming NONE would key one by "" (which the
// store now refuses outright, since the row's user_id REFERENCES users(id)).
//
// The derived tab-index rows carried the same hazard until two later layers
// closed it: Project / projectOneTab still take each row's owner from
// state.GetUserId(), but service.txTabIndexWriter stamps every upserted row
// with the COMMITTING tenant instead, and workspace_tab_owned.user_id now
// REFERENCES users(id), so a blank-owner row -- the one the store could never
// delete and the worker reconciler kept reading as a live tab -- is no longer
// storable at all. This refusal is still the first of the three, and the only
// one that stops a manager serving a document it cannot name.
//
// Nothing in the code can produce that divergence today: UpsertUserState keys
// the row by the payload's own user_id, so key and payload agree by
// construction. That is precisely why refusing is the right response rather
// than repairing -- a mismatch means the row was corrupted or hand-edited, and
// a manager that cannot say whose document it holds must not serve it.
//
// The comparison routes through userid.UserID.Matches rather than `!=` because
// `!=` is fail-OPEN on empty-vs-empty, so a blank-tenant manager would accept a
// blank-tenant payload -- the one case that used to produce the undeletable tab
// rows described above, and the one that still reaches a blank-keyed user_state
// write. Named by internal/audit.identityComparisonSites.
func (m *Manager) requireOwnState(state *leapmuxv1.UserCrdtState) error {
	if m.owner.IsZero() {
		return errBlankManagerTenant
	}
	mgrUser := m.owner
	if !mgrUser.Matches(state.GetUserId()) {
		return fmt.Errorf("user_state payload names user %q, but this manager serves %q",
			state.GetUserId(), m.owner.String())
	}
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
	// Signal readiness exactly once, before the loop begins servicing jobs.
	// The registry waits on started() before handing this manager out, so any
	// caller observing the manager post-Get routes its seed through taskCh (the
	// sole writer) instead of the pre-Start direct-write branch -- closing the
	// race the old atomic.Bool left open (Get spawned `go Start()` and returned
	// before Start had set the flag).
	m.startedOnce.Do(func() { close(m.startedChan) })
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
		case task := <-m.taskCh:
			// A caller-driven pass (SeedStateForTest, TickHousekeeping) run on
			// this goroutine -- the sole writer. Race-safety against
			// processSubmit / DiffProjectionForBatch comes from same-goroutine
			// sequencing (these are select cases on one loop, so the goroutine
			// is never mid-ValidateBatch while servicing a task);
			// commitState's swap additionally excludes the cross-goroutine
			// RLock readers (Materialized, currentEpoch, broadcast) for the
			// pointer write. A seed runs against a CLONE and is PUBLISHED, so a
			// published generation stays immutable for the lock-free readers
			// that captured it. Deferred close(done) so a panicking task does
			// not wedge the caller on <-done; the panic itself still propagates
			// (crashing the goroutine) so a buggy task is visible, but Start's
			// defer close(m.done) runs first so Stop's waiter and the caller's
			// <-done both release. A seed that panics leaves the live state
			// untouched, since nothing is published until fn returns.
			func() {
				defer close(task.done)
				task.run()
			}()
		case <-ticker.C:
			m.tickHousekeeping(ctx)
		}
	}
}

// started returns a channel that is closed the first time Start runs, before
// its select loop begins servicing jobs. Callers can wait on it to observe that
// the manager goroutine is (or was, before a Stop) servicing the loop -- a
// stronger signal than a bare atomic flag, because the registry waits on it
// before handing the manager out. The channel is never re-opened, so after Stop
// it stays closed (callers that need to distinguish "stopped" from "never
// started" consult done, which Start closes on return).
func (m *Manager) started() <-chan struct{} {
	return m.startedChan
}

// WaitForStartForTest blocks until Start has been called and its select loop is
// servicing jobs (or Start has returned). Test harnesses that spawn Start in a
// goroutine and then call SeedStateForTest must wait on this first, otherwise
// SeedStateForTest's pre-Start branch could run on the test goroutine racing
// the just-spawned manager goroutine's bare reads. The registry's Get already
// waits internally, so callers that obtain the manager via Get do not need this.
func (m *Manager) WaitForStartForTest() {
	<-m.startedChan
}

// touchActivity stamps the manager as freshly active. Called on every
// Submit / SubmitInternal entry point AND by Registry.Get when it hands out
// an existing manager, so the Registry's idle-eviction janitor only stops
// managers that genuinely haven't been referenced -- and never stops one a
// concurrent Get just returned.
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

// WaitForNudges blocks until any in-flight reconcile-nudge goroutines spawned by
// processBatch have delivered. Tests that assert on nudge delivery call this
// after Submit; WaitForAudits does NOT cover them, because nudges are tracked
// separately so Stop() can decline to wait on an uninterruptible worker send
// (see the note on nudgeWG).
func (m *Manager) WaitForNudges() {
	m.nudgeWG.Wait()
}

// Stop signals the goroutine to exit and waits for it. Any pending
// deferred-clear timers are stopped so they don't fire on a defunct
// manager. Background audit goroutines are drained before returning
// so their log breadcrumbs always make it out.
//
// Concurrent calls are safe: stopOnce closes m.stop exactly once, and both
// the presence-controller shutdown and the audit WaitGroup wait are idempotent,
// so two callers racing Stop each wait for the goroutine to exit without one
// of them panicking on a double close.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
	<-m.done
	m.presenceCtl.Shutdown()
	m.auditWG.Wait()
}

// Submit is the client-callable entrypoint. Routed through the
// goroutine.
func (m *Manager) Submit(ctx context.Context, input SubmitInput) ([]*leapmuxv1.BatchResult, error) {
	resp := make(chan submitResponse, 1)
	// Touch activity BEFORE the channel send so the idle janitor cannot evict
	// this manager in the window between the send and the stamp: once the job is
	// in submitCh the manager goroutine may pick it up and respond at any time,
	// but the stamp is what keeps SweepIdle from reaping the manager out from
	// under the in-flight submit. (Sending first then stamping inverted that
	// ordering and left a narrow race where a Submit to a manager whose
	// lastActivity had gone stale could be stranded if the janitor fired in the
	// gap.)
	m.touchActivity()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m.submitCh <- submitJob{input: input, respCh: resp}:
	}
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
	// See Submit: touch activity before the send so the idle janitor cannot
	// evict the manager between the send and the stamp.
	m.touchActivity()
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

// Materialized returns the public projection filtered to a given
// allowed-set (used by the GetMaterialized RPC, tests and one-shot callers).
//
// Only the generation CAPTURE is under m.mu; the O(all-entities) walk runs
// lock-free against it. See materializedFromState.
func (m *Manager) Materialized(filter SubscriberFilter) *leapmuxv1.UserMaterialized {
	return materializedFromState(m.capturedState(), filter)
}

// materializedFromState builds the public projection of `state`, filtered to
// `filter`'s allowed workspaces.
//
// IT HOLDS NO MANAGER LOCK, and it does not need one: `state` is a PUBLISHED
// GENERATION, which is immutable (see commitState). A caller captures the
// pointer under a brief m.mu.RLock and then pays the O(all-entities) walk with
// nothing held -- which is the whole point, because that walk used to run under
// m.projection AND m.mu.RLock, stalling every commit and broadcast for that
// user for its duration (issue #267).
//
// A FREE FUNCTION, not a method, so the type system says so: there is no
// receiver to reach m.state through, hence no way to accidentally read the LIVE
// state half-way through a walk of a captured one.
//
// The per-record proto.Clone below is NOT redundant with generation
// immutability, and must stay. Two concurrent connects would otherwise share
// record pointers, and proto.Marshal / proto.Size write each message's
// sizeCache non-atomically -- a real data race, detector or no detector. (The
// resume path DOES alias a record, at buildResumeDelta; that one is safe only
// because its records come from a per-call proto.Unmarshal and are never shared
// across subscribers. Do not generalize from it.)
func materializedFromState(state *leapmuxv1.UserCrdtState, filter SubscriberFilter) *leapmuxv1.UserMaterialized {
	out := &leapmuxv1.UserMaterialized{
		UserId:          state.GetUserId(),
		Nodes:           map[string]*leapmuxv1.NodeRecord{},
		Tabs:            map[string]*leapmuxv1.TabRecord{},
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{},
		Workspaces:      map[string]*leapmuxv1.WorkspaceContentsRecord{},
		// Read from the SAME generation as the entities below, so the snapshot
		// and the HLC that labels it are a consistent point-in-time pair by
		// construction rather than by both happening under one lock.
		MaxHlc:       HLCClone(state.GetMaxHlc()),
		CurrentEpoch: state.GetCurrentEpoch(),
	}
	roots := registeredRoots(state)
	// Build node→workspace once via BFS from each filter-allowed root,
	// avoiding the per-entry O(depth) walks that nodeWorkspace /
	// resolveTileWorkspace would otherwise do for every node and every
	// tab. Tombstoned ancestors are not skipped during descent — this
	// preserves nodeWorkspace's existing behaviour where a live node
	// whose intermediate ancestor is tombstoned still resolves to the
	// registered-root workspace above it.
	nodeWS := buildNodeWorkspaceMap(state, roots, filter)

	for wsID, ws := range state.GetWorkspaces() {
		if !filter.IsAllowed(wsID) {
			continue
		}
		out.Workspaces[wsID] = &leapmuxv1.WorkspaceContentsRecord{
			WorkspaceId: ws.GetWorkspaceId(),
			RootNodeId:  ws.GetRootNodeId(),
		}
	}
	for id, n := range state.GetNodes() {
		if !HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		if _, ok := nodeWS[id]; ok {
			out.Nodes[id] = cloneNode(n)
		}
	}
	for id, t := range state.GetTabs() {
		if _, ok := nodeWS[t.GetTileId().GetValue()]; ok {
			out.Tabs[id] = cloneTab(t)
		}
	}
	for id, fw := range state.GetFloatingWindows() {
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
func buildNodeWorkspaceMap(state *leapmuxv1.UserCrdtState, roots rootSet, filter SubscriberFilter) map[string]string {
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
	// The tenancy floor. A submit no longer names the tenant it is addressed to
	// -- the manager it landed on IS the tenant, keyed and bounds-checked by
	// Registry.Get -- so a cross-tenant submit is unspellable and there is
	// nothing left to compare. What remains is the manager with NO tenant:
	// NewManager("") would commit ops into a CRDT belonging to nobody and key a
	// user_state row by "". Registry.Get's blank-key refusal keeps production
	// from ever reaching here; this arm covers a manager built directly with
	// NewManager (tests, and any future in-process caller).
	//
	// It has to live here rather than only in the journal: service.crdtJournal's
	// errBlankTenant guards the REAL journal, so a manager wired to a test or
	// in-memory journal would otherwise commit blank-tenant state unopposed.
	if m.owner.IsZero() {
		return submitResponse{err: errBlankManagerTenant}
	}

	// This submit's read-modify-write of m.state runs on the manager goroutine,
	// which is the sole writer -- so the bare m.state map reads in ValidateBatch
	// / DiffProjectionForBatch and the commit swap below need no extra lock
	// beyond m.mu (which guards the RLock readers and the swap itself).
	// Workspace lifecycle create/delete now flow through SubmitInternal as
	// SetWorkspaceRegisterOp / TombstoneWorkspaceOp, so no out-of-band writer
	// remains.

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
	res, working := ValidateBatch(ctx, m.state, batch.GetOps(), in.Internal, in.PrincipalID, scopedAuthChecker(m.auth, in.WorkerScope))
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
	if err := m.commit(ctx, in, batch, working, res); err != nil {
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
	// under in.Internal so the legitimate WorkerTabInventory path
	// (worker reconnect, hub-driven tombstones) doesn't drown it
	// out.
	//
	// The audit fires in a background goroutine: m.auth.CanUseWorker
	// hits the DB (workers) and we don't want
	// to serialise every other client for that user behind that lookup.
	// preState is the pre-commit state captured above; once stored in
	// m.state's history it's immutable, so the goroutine can safely
	// read from it after the manager mutex is released. auditWG
	// drains on Stop() so log breadcrumbs always make it out.
	if containsTombstoneTab(batch) {
		// Resolve the tombstoned tabs' worker ids from preState NOW, before
		// spawning, so the audit goroutine captures only this small map instead
		// of the whole (potentially multi-MB) UserCrdtState. After m.state =
		// working below, preState is otherwise GC-eligible; capturing it in the
		// goroutine would pin the old generation for the CanUseWorker DB lookup.
		workerIDs := tombstonedTabWorkerIDs(batch, preState)
		if !in.Internal {
			m.auditWG.Add(1)
			go func() {
				defer m.auditWG.Done()
				// Recovered for the same reason sendq.Writer.run and
				// workermgr.SendPump.drain recover their transports: this
				// goroutine is owned by the Hub process, not by the submit that
				// spawned it, so a panic in the audit's DB lookup or its log
				// formatting would take down every user's CRDT manager, every
				// worker link and every open connection -- to salvage one log
				// breadcrumb about one tombstoned tab. Losing the breadcrumb is
				// the smaller failure, so it is reported and the process
				// continues. auditWG.Done stays deferred FIRST, so Stop() is
				// released either way.
				defer panicsafe.RecoverAndLog(m.logger,
					"crdt: recovered from a panicking orphan tab audit",
					"batch_id", batch.GetBatchId(), "principal", in.PrincipalID)
				m.auditOrphanTabTombstones(in, batch, res, workerIDs)
			}()
		}
		// Nudge each affected worker to reconcile now rather than on its hourly
		// tick. Reuses the map the audit already computed -- the tombstoned tabs'
		// hosting workers ARE the set that has local state to release -- so this
		// costs no extra query.
		//
		// NOT gated on !in.Internal the way the audit is, and the gate now sits
		// on the audit alone so that is actually true: an internal batch is
		// precisely how a workspace delete tombstones its tabs
		// (applyLifecycleDelete submits via SubmitInternal), and a worker that has
		// just told us its inventory is the one most able to act on a nudge. While
		// the nudge sat inside the !in.Internal block, deleting a workspace never
		// nudged its live worker at all, so the agent subprocesses ran until the
		// worker's hourly tick -- exactly the gap the nudge exists to close, since
		// the frontend's own per-worker CleanupWorkspace RPC is fire-and-forget.
		//
		// Dispatched on a goroutine because this is the ONE goroutine that
		// serialises every CRDT submit for this user: every later tab
		// open/move/close, on any worker, queues behind whatever runs here.
		// The send itself is cheap now -- NudgeReconcile reaches the worker
		// through Conn.SendControl, which enqueues into a bounded sendq and
		// does no network I/O -- but the path still takes workermgr's registry
		// lock and, on a refused control frame, fences the connection. Keeping
		// all of that off the submit path costs one goroutine. Mirrors the
		// audit's rationale above.
		if m.reconcileNudger != nil {
			seen := make(map[string]struct{}, len(workerIDs))
			targets := make([]string, 0, len(workerIDs))
			for _, workerID := range workerIDs {
				if workerID == "" {
					continue
				}
				if _, dup := seen[workerID]; dup {
					continue
				}
				seen[workerID] = struct{}{}
				targets = append(targets, workerID)
			}
			if len(targets) > 0 {
				nudger := m.reconcileNudger
				// Deliberately NOT tracked by auditWG, unlike the audit above.
				// auditWG is drained by Stop() because the audit has something to
				// salvage -- its log breadcrumb -- and its DB lookup is bounded by
				// OrphanAuditLookupTimeout so that drain terminates. A nudge has
				// neither: it is best-effort, and losing one on shutdown costs only
				// the delay until the worker's next reconcile tick, so making
				// Stop() wait on it would buy nothing. nudgeWG exists so tests can
				// await delivery deterministically; Stop() skips it.
				m.nudgeWG.Add(1)
				go func() {
					defer m.nudgeWG.Done()
					// Recovered like the audit above, and for a surface that has
					// grown rather than shrunk: NudgeReconcile now takes
					// workermgr's registry lock and may fence a connection whose
					// control frame was refused. A panic anywhere under there is
					// one worker's problem, and letting it unwind out of an
					// unowned goroutine would crash the Hub for every user. The
					// remaining nudges in `targets` go with it -- the loop is
					// abandoned, not resumed -- which costs each of those workers
					// only the delay until its next reconcile tick, exactly what
					// an offline worker already pays.
					defer panicsafe.RecoverAndLog(m.logger,
						"crdt: recovered from a panicking reconcile nudge",
						"workers", targets)
					for _, workerID := range targets {
						nudger.NudgeReconcile(workerID)
					}
				}()
			}
		}
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

// runDedup checks the batch's batch_id against user_recent_batch_ids.
// Outcomes:
//   - transient store error (err non-nil): the caller propagates it as a
//     retryable Submit error rather than a permanent rejection.
//   - immediate rejection (*result non-nil): returned verbatim.
//   - full hit (row non-nil, result nil): caller reconstructs the
//     original CommittedOps from the cached canonical HLC range.
//   - miss (all nil): caller proceeds with assigning canonical HLCs.
func (m *Manager) runDedup(ctx context.Context, in SubmitInput, batch *leapmuxv1.OpBatch) (*leapmuxv1.BatchResult, *RecentBatchRecord, error) {
	row, err := m.journal.LookupRecentBatchID(ctx, m.owner.String(), batch.GetBatchId())
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
func (m *Manager) commit(ctx context.Context, in SubmitInput, batch *leapmuxv1.OpBatch, working *leapmuxv1.UserCrdtState, res ValidationResult) error {
	// DiffProjectionForBatch skips the per-tab chain walks for tabs the
	// batch cannot possibly transition. Non-structural batches (the
	// common case: user-triggered tab open/move/close) re-project only
	// the tabs they explicitly name; structural batches fall back to
	// full Project + Diff. The latter is at most as expensive as the
	// pre-existing path (which projected both states unconditionally), so
	// this is a strict improvement.
	diff := DiffProjectionForBatch(m.state, working, batch.GetOps())

	hash, err := BatchBodyHash(batch)
	if err != nil {
		return fmt.Errorf("hash batch body: %w", err)
	}
	// Persist the per-batch AffectedEntities (the {pre, post} workspace
	// transitions ValidateBatch computed) alongside the OpBatch. The resume
	// path decodes them to replay the SAME pre/post stable-visibility
	// classification the live broadcast applies to this very batch, so the two
	// catch-up paths cannot drift (see buildResumeDelta).
	transitions := AffectedEntitiesToProto(res.AffectedEntities, working)
	if err := m.journal.CommitBatch(ctx, CommitBatch{
		UserID:      m.owner.String(),
		Batch:       batch,
		PrincipalID: in.PrincipalID,
		Epoch:       m.state.GetCurrentEpoch(),
		Dedup: DedupEntry{
			BatchID:           batch.GetBatchId(),
			BodyHash:          hash,
			CanonicalFirstHLC: HLCClone(batch.GetOps()[0].GetCanonicalHlc()),
			OpCount:           int64(len(batch.GetOps())),
			ExpiresAt:         m.now().Add(DedupTTL),
		},
		IndexDiff:   diff,
		Transitions: transitions,
	}); err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}

	m.commitState(working)
	return nil
}

// commitState is the SOLE mutator of m.state: it swaps in a new generation
// under m.mu.Lock, excluding the RLock readers (Materialized, currentEpoch,
// broadcast, WithStateRLock) for the duration of the pointer swap. Routing
// every state replacement through this one method makes the single-writer
// invariant structurally visible -- `m.state = ` appears exactly once in the
// package, on the line below, and every publisher (Bootstrap, commit,
// compaction, the epoch bump, housekeeping, SeedStateForTest) reaches it
// through here. A future debug path that tried to assign m.state directly would
// have to add a SECOND assignment, which is exactly the review checkpoint the
// invariant needs -- and, because generations share maps (see below), exactly
// the change that would break the lock-free readers.
//
// A PUBLISHED GENERATION IS NEVER MUTATED. Every state change builds a new
// generation and swaps it in here: commit via CloneStateForBatch, compaction
// and the epoch bump via CloneState / nextStateGeneration, SeedStateForTest via
// CloneState. Nothing writes through the live pointer.
//
// That is a load-bearing guarantee, not tidiness. It is what lets a reader
// capture the generation pointer under a brief RLock and then walk it holding
// NO lock at all -- which is how a cold /ws/userevents connect builds its
// O(all-entities) baseline without stalling the commit pipeline (see
// materializedFromState and registerForFallback), and what manager_audit.go's
// "once stored in m.state's history it's immutable" already assumes.
//
// The subtle half is that generations SHARE maps: CloneStateForBatch aliases
// the entity map of any kind a batch does not touch, and nextStateGeneration
// aliases all four. So an in-place `delete` on the current generation mutates
// older ones too. That is why compaction publishes its pruned clone rather than
// re-pruning the live state.
func (m *Manager) commitState(state *leapmuxv1.UserCrdtState) {
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
}

// capturedState returns the current published generation.
//
// This is THE read for anything that wants to walk the state without holding a
// lock across the walk: a published generation is never mutated (see
// commitState), so the returned pointer is a stable point-in-time value rather
// than a live view. It is O(1) — no clone — and the only lock it takes is the
// RLock around the pointer read itself.
//
// A named operation rather than three hand-spelled copies of
// `m.mu.RLock(); s := m.state; m.mu.RUnlock()`, which is what capture-the-
// generation had become. It deliberately does NOT cover a window that captures
// AND does something else under the same RLock — registerForFallbackLocked's
// does, and that atomicity is exactly what its no-gap/no-duplicate proof rests
// on, so it keeps its own explicit lock.
//
// The caller MUST NOT mutate what it gets back; one that needs a writable copy
// takes State(), which deep-clones.
func (m *Manager) capturedState() *leapmuxv1.UserCrdtState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// State returns a deep clone of the current state.
//
// TEST-ONLY: `rg '\.State\(\)' backend/ --glob '!*_test.go'` finds no production
// caller. Its historical reason for existing — "callers that need to retain the
// state past the manager's RLock window" — was retired by the generation-
// immutability invariant: capturedState() retains a generation for free, and
// what remains here is a genuinely WRITABLE copy, which is what tests take it
// for. The clone runs outside the lock for the same reason; only the pointer
// read needs one.
func (m *Manager) State() *leapmuxv1.UserCrdtState {
	return CloneState(m.capturedState())
}

// WithStateRLock runs `fn` against the live in-memory state under
// m.mu.RLock so the caller avoids a multi-MB CloneState allocation when
// it only needs a synchronous walk (enumeration, projection,
// computation).
//
// The pointer MAY escape `fn` — a published generation is immutable, so what
// escapes is a stable point-in-time value, not a live view (see commitState).
// The same guarantee is what lets registerForFallback capture the generation
// under its own m.mu.RLock and then build an O(all-entities) baseline from it
// holding no lock at all; among this helper's own callers, the immutability
// suite's captureGeneration is the one that relies on the escape. What must NOT
// happen is MUTATING the escaped state; a caller that needs a writable copy
// takes State(), which deep-clones.
//
// Note the pointer stops tracking the manager the moment it escapes: a later
// commit publishes a new generation and the captured one keeps the shape it had.
// That is the point on the baseline path, and a bug for a caller that wanted
// "current" — such a caller should re-enter rather than cache.
func (m *Manager) WithStateRLock(fn func(state *leapmuxv1.UserCrdtState)) {
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

// SeedStateForTest runs fn against a CLONE of m.state as the sole writer and
// publishes the result, so the write cannot race the manager goroutine's bare
// m.state reads in ValidateBatch / DiffProjectionForBatch. It is a TEST-ONLY
// seam for seeds that genuinely
// cannot be a valid op batch — e.g. bumping CurrentEpoch to exercise a
// stale-epoch gate (no op writes CurrentEpoch; only maybeAdvanceEpoch does) or
// constructing a record with hand-picked LWW HLCs that bypass the LWW-merge
// path. Every workspace-record seed that CAN be an op batch should go through
// SubmitInternal as a SetWorkspaceRegisterOp, mirroring the production
// lifecycle create path.
//
// fn receives a CLONE, not the live generation, and the clone is published via
// commitState. A published generation is immutable (see commitState), and a
// seed that edited m.state in place would break that for every reader that
// captured the pointer — including a cold subscriber building its baseline
// lock-free. The clone also means a seed that panics part-way leaves the live
// state untouched instead of half-written.
//
// Once Start has been called, fn runs ON the manager goroutine (via taskCh),
// the same goroutine that owns the bare reads, so there is
// no cross-goroutine write to race them. The registry waits on started()
// before handing the manager out, so any caller that observes the manager via
// Get routes its seed through the goroutine (closing the race an off-goroutine
// pre-Start write would otherwise open). Before Start has been called (the
// registry-factory shape, where the seed runs inside the factory closure
// before Get spawns the goroutine), fn runs on the calling goroutine,
// mirroring Bootstrap's no-concurrent-reader assumption.
//
// fn MUST NOT call back into Submit / SubmitInternal / SeedStateForTest /
// TickHousekeeping: those channel sends would be made from the manager
// goroutine that is supposed to receive on them, deadlocking the sole consumer.
// (TickHousekeeping joined that list when it started routing through taskCh.)
//
// After Stop, the goroutine has exited and the send falls through to <-m.done,
// so a post-Stop SeedStateForTest returns without running fn (it cannot race,
// but it also cannot seed) rather than wedging the caller.
//
// Test-only: production state writes flow exclusively through SubmitInternal.
func (m *Manager) SeedStateForTest(fn func(state *leapmuxv1.UserCrdtState)) {
	m.runOnManagerGoroutine(func() {
		next := CloneState(m.state)
		fn(next)
		m.commitState(next)
	})
}

// runOnManagerGoroutine runs `run` on the manager goroutine and returns once it
// has completed, so a caller can rely on its effects.
//
// This is the one home for the started/stopped/pre-Start negotiation that both
// caller-driven passes need (SeedStateForTest and TickHousekeeping), rather
// than a copy each:
//
//   - Started: hand the task to the goroutine and wait. The select on m.done
//     returns if the goroutine has since exited (Stop) instead of wedging the
//     caller on the unbuffered channel.
//   - Not started: no manager goroutine exists to race, so run inline (mirrors
//     Bootstrap). Safe only from the registry-factory shape, where this runs
//     inside the factory closure before Get spawns Start; a direct caller that
//     spawned `go Start()` and then reached this branch would race the
//     goroutine it just launched, because startedChan is closed INSIDE Start --
//     an open channel does not prove no goroutine exists. Route through the
//     registry instead.
//
// `run` executes on the sole writer, so it may read m.state bare and publish
// through commitState; it MUST NOT send on submitCh / internalCh / taskCh,
// which would deadlock the consumer it is running on.
//
// The same rule binds the CALLER, and that half is easier to miss: this
// function is itself a taskCh send, so calling it FROM the manager goroutine
// deadlocks it permanently -- no commits, no broadcasts, no subscribes for that
// user, and Stop never returns, because the goroutine is blocked in the send
// and can never reach the receive. The `<-m.done` escape does not help; that
// arm only fires once the goroutine has already exited. The ticker arm inside
// Start is correct precisely because it calls the unexported tickHousekeeping
// directly rather than coming back through here.
func (m *Manager) runOnManagerGoroutine(run func()) {
	select {
	case <-m.startedChan:
		done := make(chan struct{})
		select {
		case m.taskCh <- managerTask{run: run, done: done}:
			<-done
		case <-m.done:
		}
	default:
		run()
	}
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
