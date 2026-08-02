package crdt

import (
	"context"
	"errors"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/proto"
)

// Subscriber registration and the delta-resume protocol.
//
// One file for the whole subscribe family because its members are a single
// mechanism, not a layer: SubscribeWithACL is the only production entry point,
// and it reaches FALLBACK and RESUME through the same register seam
// (registerSubscriber / makeUnsub) that the plain Subscribe path uses, so the
// presence-refcount invariant has exactly one home. Splitting only the resume
// state machine out would have put half of that seam in each file.
//
// The lock plan the whole file turns on is documented on SubscribeWithACL.

// SubscribeWithACL resolves a subscriber's workspace filter, then registers it and
// emits the bootstrap frame -- a ResumeDelta when the client's cursor is still
// within the hub's compaction window, or a full UserMaterialized snapshot
// (FALLBACK) otherwise. Every /ws/userevents connect goes through this: a
// first-time connect passes a nil cursor and gets a full snapshot via the
// FALLBACK arm, which is byte-identical to the legacy full-snapshot subscribe.
//
// resolve reads the DB ACL and returns the allowed workspace set; it runs while
// this holds subscribeExpandMu, the SAME lock ExpandSubscribersForWorkspace
// holds across a workspace-create expansion. That serialization closes the
// resolve-then-register TOCTOU: a concurrent create of a workspace the user
// owns is ordered either FULLY before resolve() -- so resolve reads the
// post-commit ACL and the workspace is already included -- or FULLY after the
// register below -- so the create's expand pass sees the now-registered
// subscriber. Without it, a filter resolved from the pre-commit ACL could be
// registered after the whole expansion finished, and the expand (which only
// visits already-registered subscribers) would miss it, so the subscriber
// would never see the new workspace until it reconnected.
//
// resolve's error is returned unregistered so the caller can reject the
// connection before streaming any events. Lock order
// subscribeExpandMu -> projection -> m.mu is preserved on every path where
// more than one is held (the FALLBACK arms hold subscribeExpandMu + projection
// across the register; Subscribe, called from the FALLBACK seam, takes only
// projection and m.mu).
//
// RESUME: when the cursor is non-zero, above op_retention_watermark (the
// lagging floor below which op batches may have been deleted), and the
// epoch is unchanged, this registers the subscriber and returns a ResumeDelta
// (the post-cursor op tail, filtered to the subscriber's allowed set) instead
// of a full snapshot. The register + until capture run under subscribeExpandMu
// and a brief m.projection hold (closing the resolve→register TOCTOU and the
// until/broadcast dual-delivery race); both are then RELEASED, and the journal
// tail read (m.journal.ListBatchesAfter) + visibility filter run under NEITHER
// subscribeExpandMu NOR m.projection. That is safe because the broadcast
// straddle is closed by REGISTRATION ORDER plus resumeSuppressThrough: the
// just-registered subscriber is in the snapshot subs for any commit's
// broadcastBatch, so a commit landing during the tail read is delivered as a
// normal broadcast frame right after the delta (and batches at or below until
// are suppressed on the live path so they ship only in the delta). The tail is
// therefore a strict lower bound; the broadcast stream fills anything newer.
// Releasing the locks for the scan means a reconnect's DB scan no longer stalls
// that user's workspace lifecycle RPCs (subscribeExpandMu) or other tabs' live
// commit broadcasts (m.projection) for the scan's duration.
//
// The delta's max_hlc is computed over the FRAMES THIS DELTA EMITS, never read
// from live m.state. That is what keeps a commit landing during the tail read
// from being skipped: it advances m.state.MaxHlc but is delivered as a
// broadcast frame AFTER the delta, so advertising the live max here would let a
// disconnect-before-broadcast lose it on the next resume.
//
// Precisely: max_hlc is the max over each emitted frame's own HLC — visible
// ops' canonical HLCs, AND the at_hlc carried by each materialized/removed
// transition frame. A transition frame's at_hlc is its BATCH's last-op HLC (see
// emitCatchUpFrames), so for a batch whose ops are partly filtered out, max_hlc
// can exceed the last op the subscriber actually received. That is sound only
// because the cursor is compared against a strictly-greater scan and a filtered
// entity that later becomes visible arrives as a full EntityMaterialized record
// rather than as its historical ops — it does NOT hold as the stronger
// "never exceeds an op it received", and a client that reconnects under a WIDER
// workspace_ids filter than the one the cursor was minted under can miss ops.
// The persisted cursor is per-user, not per-filter. Two in-tree callers DO
// narrow the filter -- remoteipc.HubEventStreamer and remote.Client both forward
// a workspace_ids set -- but neither ever PRESENTS a cursor (both hard-code
// nil/0 and are stateless per call), and the browser, which does present one,
// always subscribes to all owned workspaces. So the pairing that would bite --
// a cursor minted under a narrow filter and replayed under a wider one -- has no
// producer today. It is a constraint to preserve, not a live bug; making it
// mechanical needs the mint-time filter carried alongside the cursor.
//
// ctx threads the request's cancellation into the journal tail read so a
// client that disconnects mid-resume aborts the (potentially multi-page, up to
// MaxResumeDeltaOps) DB scan instead of completing it only to discard the result.
//
// FALLBACK: when cursor is nil/zero, at or below op_retention_watermark
// (the journal can no longer reconstruct the delta), the epoch is stale, the
// post-cursor tail exceeds MaxResumeDeltaOps / MaxResumeDeltaBytes, or the BUILT
// delta exceeds MaxResumeDeltaFrameBytes (the wire-frame gate in
// buildResumeDelta), this returns a SubscribeOutcome whose Mode is
// SubscribeInitial (carrying the full UserMaterialized snapshot the caller sends
// as the `initial` frame).
func (m *Manager) SubscribeWithACL(
	ctx context.Context,
	sub *Subscriber,
	cursor *leapmuxv1.HLC,
	clientEpoch int64,
	resolve func() (map[string]bool, error),
) (SubscribeOutcome, error) {
	// Lock plan (release order matters — see the lock-ordering note on the
	// type). subscribeExpandMu closes the resolve→register TOCTOU with
	// ExpandSubscribersForWorkspace; it is needed ONLY across resolve + the
	// register/FALLBACK decision, then RELEASED before the RESUME tail read so
	// a reconnect's (potentially multi-page) DB scan does not stall that
	// user's workspace-create/delete lifecycle RPCs. m.projection serializes
	// the FALLBACK arm's register+materialized-baseline against broadcasts
	// (the Subscribe invariant), and on the RESUME arm is held only across the
	// brief until-capture+register window (so broadcastBatch cannot dual-
	// deliver a batch that also lands inside until). The RESUME scan itself
	// does NOT hold m.projection — registration order plus
	// resumeSuppressThrough close the straddle, and releasing projection for
	// the scan means a reconnect no longer stalls every other tab's live
	// commit broadcasts for the scan's duration.
	// The locked section is a closure so its unlocks run via defer. resolve()
	// reaches the store, and net/http RECOVERS a handler panic and keeps the
	// process serving -- so a panic under subscribeExpandMu would leak it and
	// wedge every later /ws/userevents connect plus every workspace
	// create/delete for this user, silently and until restart. Explicit unlocks
	// cover the return paths; only defer covers the unwind.
	// out stays at its zero value (mode subscribeModeInvalid) unless the FALLBACK
	// arm fills it in, so its mode IS the record of which arm ran -- no separate
	// bool to keep in step with it.
	var (
		out    SubscribeOutcome
		until  *leapmuxv1.HLC
		filter SubscriberFilter
	)
	if err := func() error {
		m.subscribeExpandMu.Lock()
		defer m.subscribeExpandMu.Unlock()

		allowed, err := resolve()
		if err != nil {
			return err
		}
		sub.Filter = NewSubscriberFilter(allowed)

		if !m.decideResume(cursor, clientEpoch) {
			// FALLBACK: identical to Subscribe (full snapshot under projection +
			// m.mu.RLock). subscribeLocked needs m.projection held across the
			// register+materialized clone so the baseline cannot straddle a
			// concurrent broadcast's filter mutation — the same reason Subscribe
			// holds it. subscribeExpandMu stays held through the register to keep
			// the expand TOCTOU closed. LIFO defer order preserves the
			// projection-then-expand release order the lock plan requires.
			m.projection.Lock()
			defer m.projection.Unlock()
			out = m.fallbackOutcome(sub)
			return nil
		}

		// RESUME: register the subscriber via the same seam Subscribe/FALLBACK
		// use (so the presence-refcount invariant has one home), then build the
		// delta. subscribeLocked would also compute a materialized snapshot the
		// resume does not need, so call only its register half.
		// subscribeExpandMu is still held so the register stays serialized
		// against a concurrent expand; it is released on return from this
		// closure, before buildResumeDelta's DB scan.
		until, filter = m.registerForResume(sub)
		return nil
	}(); err != nil {
		return SubscribeOutcome{}, err
	}
	if out.Mode() == SubscribeInitial {
		return out, nil
	}

	// buildResumeDelta owns the unsub handle (makeUnsub is idempotent, so it
	// doubles as teardown for the FALLBACK re-entry the caller performs).
	// buildResumeDelta returns a resumeScanResult; the caller (this function)
	// owns the FALLBACK re-entry, so buildResumeDelta no longer takes the
	// resolve/unsub teardown callbacks.
	req := resumeRequest{
		sub:         sub,
		filter:      filter,
		cursor:      cursor,
		until:       until,
		clientEpoch: clientEpoch,
	}
	res := m.buildResumeDelta(ctx, req)
	switch res.kind {
	case resumeScanDelta:
		return res.outcome, nil
	case resumeScanFallback:
		// buildResumeDelta signaled FALLBACK (over-budget / corrupt / post-scan
		// compaction-or-epoch drift). It already tore down the registration;
		// re-enter the FALLBACK seam, which re-resolves + re-registers fresh.
		return m.resumeFallback(sub, resolve)
	case resumeScanError:
		// The registration is already torn down (unsub fired inside
		// buildResumeDelta); surface the connect failure.
		return SubscribeOutcome{}, res.err
	default:
		// resumeScanInvalid — a return path that forgot to set `kind`. Refuse
		// the connect rather than ship a zero-value outcome.
		return SubscribeOutcome{}, fmt.Errorf("resume: buildResumeDelta returned no verdict (kind=%d)", res.kind)
	}
}

// decideResume is the RESUME-vs-FALLBACK verdict against live state: the cursor
// is non-zero, strictly above op_retention_watermark (the lagging floor below
// which op batches may have been deleted, so the journal can no longer
// reconstruct the delta), and the client's epoch matches the live epoch.
// op_retention_watermark — not compaction_watermark — gates resume because the
// resume contract is "can the server still produce the delta?", which is a
// question about op-batch availability, not tombstone pruning. Tombstones are
// pruned at the tighter compaction_watermark (= max_hlc) for correctness, and
// that prune does not by itself remove any op batch (see maybeCompact, where
// DropThrough lags at op_retention_watermark while PruneTombstonesAtOrBelow
// runs at compaction_watermark). Read under a brief m.mu.RLock; the post-scan
// re-check in buildResumeDelta re-validates because the unlocked scan can race
// a compaction or epoch bump (see buildResumeDelta).
func (m *Manager) decideResume(cursor *leapmuxv1.HLC, clientEpoch int64) bool {
	if cursor == nil || HLCIsZero(cursor) {
		return false
	}
	// Retention floor, applied ALONGSIDE the stored watermark.
	//
	// op_retention_watermark only advances while the user is committing:
	// maybeCompact's tick short-circuits once compaction_watermark reaches
	// max_hlc, so a dormant account's floor freezes at (last activity - TTL)
	// while the cleanup job keeps sweeping its rows. Gating on the stored
	// watermark alone would then admit a cursor whose batches the sweep had
	// already deleted, and the resume would ship a silently short tail --
	// ListBatchesAfter reports deleted-below rows as an ordinary short result,
	// not an error.
	//
	// Both sides call OpRetentionCutoffPhysicalMs, so this is the same number
	// the sweep deletes below, in the same (HLC physical) domain.
	//
	m.mu.RLock()
	defer m.mu.RUnlock()
	return HLCCmp(cursor, m.opRetentionFloorLocked()) > 0 &&
		clientEpoch == m.state.GetCurrentEpoch()
}

// opRetentionFloorLocked returns the effective HLC floor a resume cursor must
// sit strictly above: the greater of the PERSISTED op_retention_watermark and
// the live wall-clock cutoff the cross-user sweep deletes below.
//
// One expression for one concept. The two floors were previously applied as two
// differently-SHAPED tests at the call site -- a physical-only `<` against the
// cutoff, then an HLC compare against the stored watermark -- which read as two
// unrelated rules rather than "above every floor that deletes".
//
// Both are needed, and neither subsumes the other:
//
//   - the STORED watermark is authoritative while the user is active, but it
//     only advances while they are committing (maybeCompact short-circuits once
//     compaction_watermark reaches max_hlc), so a dormant account's freezes at
//     (last activity - TTL) while the cleanup job keeps sweeping its rows;
//   - the WALL-CLOCK cutoff tracks the sweep exactly, and is the only one that
//     moves for an account with no resident manager at all -- which is precisely
//     the account whose stored value is stale.
//
// Both sides call OpRetentionCutoffPhysicalMs, so this is the same number the
// sweep deletes below, in the same (HLC physical) domain.
//
// The cutoff is compared as a physical-only HLC, which preserves the STRICT
// less-than the sweep uses (`physical_ms < cutoff`): a batch at exactly the
// cutoff survives, so a cursor there stays resumable, and a zero TTL does not
// refuse every cursor before the scan starts. A cursor at the cutoff physical
// with logical 0 and an empty client id compares equal to this floor and is
// therefore refused by the caller's strict `>` -- the same answer the previous
// two-test form gave, since it required `physical >= cutoff` AND
// `HLCCmp(cursor, stored) > 0`.
//
// Deliberately NOT read by maybeCompact's DropThrough. That is a DELETION floor
// whose value is also persisted as op_retention_watermark, so routing it through
// this wall-clock-derived expression would change both what compaction deletes
// and what the state blob then advertises. This is a read-side unification only.
//
// Caller must hold m.mu (read or write).
func (m *Manager) opRetentionFloorLocked() *leapmuxv1.HLC {
	floor := m.state.GetOpRetentionWatermark()
	cutoff := &leapmuxv1.HLC{Physical: OpRetentionCutoffPhysicalMs(m.now(), m.opRetentionTTL)}
	if HLCCmp(cutoff, floor) > 0 {
		floor = cutoff
	}
	// Never above the tombstone-prune line: op batches at or below
	// compaction_watermark are still resumable when they survive the lagging
	// deletion floor, and clamping keeps this from refusing them.
	if wm := m.state.GetCompactionWatermark(); wm != nil && HLCCmp(floor, wm) > 0 {
		floor = wm
	}
	return floor
}

// registerForResume is the register+until-capture window of the RESUME arm.
// until capture + register MUST be atomic w.r.t. broadcastBatch: commitState
// advances MaxHlc under m.mu alone, then broadcastBatch takes m.projection.
// Capturing until under a separate m.mu.RLock after an unlocked Add left a
// window where a concurrent commit could (1) advance MaxHlc, (2) broadcast the
// batch to the just-registered sub, and (3) still fall inside until —
// dual-delivering the same catch-up frames via live Send and the delta.
// Holding m.projection across until+register closes that window against
// broadcastBatch; resumeSuppressThrough then suppresses any in-flight broadcast
// whose HLC is <= until (commitState may have run during the brief projection
// hold before until was read).
//
// Caller MUST hold subscribeExpandMu and NOT m.projection; this method takes
// m.projection + m.mu.RLock and releases both. Returns the captured until
// high-water and a snapshot of the subscriber's filter safe to hand to the
// (lock-free) resume scan (SubscriberFilter is immutable after install — see
// its type doc).
func (m *Manager) registerForResume(sub *Subscriber) (until *leapmuxv1.HLC, filter SubscriberFilter) {
	m.projection.Lock()
	defer m.projection.Unlock()
	// BOTH unlocks are deferred, for the reason SubscribeWithACL's lock plan
	// gives: a recovered handler panic leaves the process serving, so a read
	// lock leaked while unwinding out of registerSubscriber (which reaches
	// subscribers.Add and presenceCtl.OnConnect) would block every later
	// commitState, decideResume and materializedLocked for this user forever.
	func() {
		m.mu.RLock()
		defer m.mu.RUnlock()
		until = HLCClone(m.state.GetMaxHlc())
		sub.resumeSuppressThrough = until
		m.registerSubscriber(sub)
	}()
	filter = sub.Filter
	return until, filter
}

// resumeRequest bundles the lock-free resume scan's inputs, keeping the
// buildResumeDelta signature to (ctx, req) instead of a primitive-obsessed
// param list.
type resumeRequest struct {
	sub         *Subscriber
	filter      SubscriberFilter
	cursor      *leapmuxv1.HLC
	until       *leapmuxv1.HLC
	clientEpoch int64
}

// resumeScanKind discriminates the three outcomes buildResumeDelta can report.
// An explicit kind (rather than inferring from outcome.mode) keeps the scan's
// verdict separate from the payload it carries: a FALLBACK signal and an error
// both carry a zero-value outcome, and only the kind says which of the two it
// is. (SubscribeMode reserves its own zero value for the same class of hazard —
// see subscribeModeInvalid.)
type resumeScanKind int

const (
	// resumeScanInvalid is the ZERO VALUE, deliberately not a real outcome. If
	// a future return path forgets to set `kind`, it lands here and hits the
	// switch's default error arm — loudly — instead of reading as "delta ready"
	// and shipping a nil ResumeDelta with a nil unsub (which would leak the
	// just-registered subscriber and its presence refcount).
	resumeScanInvalid resumeScanKind = iota
	resumeScanDelta
	resumeScanFallback
	resumeScanError
)

// resumeScanResult is what buildResumeDelta returns: a discriminating `kind`
// plus exactly one payload — `outcome` on resumeScanDelta, `err` on
// resumeScanError, and neither on resumeScanFallback (which carries no data:
// buildResumeDelta has already torn the registration down, and the caller
// re-resolves and re-registers from scratch).
type resumeScanResult struct {
	kind    resumeScanKind
	outcome SubscribeOutcome
	err     error
}

// buildResumeDelta constructs the RESUME outcome for an already-registered
// subscriber: page the post-cursor op tail out of the journal, filter it to
// the subscriber's current allowed workspaces, and wrap it as a ResumeDelta.
// SubscribeWithACL has ALREADY released subscribeExpandMu and m.projection on
// this path — see SubscribeWithACL's lock plan: the broadcast straddle is
// closed by REGISTRATION ORDER plus resumeSuppressThrough (the subscriber is
// registered before this call, so a commit landing during the scan is
// broadcast to it as a normal frame when HLC > until — the tail is a strict
// lower bound), so the scan + filter hold NEITHER subscribeExpandMu NOR
// m.projection and therefore do not stall workspace lifecycle RPCs or other
// tabs' live broadcasts.
//
// Returns a resumeScanResult. The caller owns FALLBACK re-entry: on
// over-budget / corrupt / post-scan compaction-or-epoch invalidation / a built
// delta over MaxResumeDeltaFrameBytes, this tears down the registration (unsub)
// and returns a bare resumeScanFallback (which carries no payload -- the caller
// re-resolves and re-registers from scratch through resumeFallback); on a hard
// tail-read error it tears down + returns the error; on success it returns the
// delta outcome.
func (m *Manager) buildResumeDelta(ctx context.Context, req resumeRequest) resumeScanResult {
	unsub := makeUnsub(req.sub, m)
	// Page the post-cursor tail out of the journal, capped at the register-time
	// high-water (`until`). ctx is the request context: a client disconnect
	// cancels it and aborts the (potentially multi-page, up to MaxResumeDeltaOps)
	// scan rather than completing it for a dead socket.
	//
	// Each ResumeBatch pairs the batch with its persisted per-entity workspace
	// transitions, so this path can replay the SAME pre/post stable-visibility
	// classification the live broadcast applies — emitting
	// entity_materialized / entity_removed WatchUserEvent frames for entities
	// that crossed the subscriber's visibility boundary during the gap, not
	// just the current-workspace-filtered raw ops. That eliminates the
	// ghost-record / incomplete-record divergence (#357): the two catch-up
	// paths read the same persisted transitions through the same
	// emitCatchUpFrames planner. The materialized frame reads a PERSISTED
	// per-batch record snapshot (captured at commit), so it ships the same
	// record broadcast would have shipped — not a current live-state clone a
	// later tail batch may have since overwritten.
	tail, corruptRows, tailErr := m.journal.ListBatchesAfter(ctx, m.owner.String(), req.cursor, req.until, MaxResumeDeltaOps, MaxResumeDeltaBytes)
	// Log every corrupt row the scan reported so the (rare) storage damage is
	// observable, whichever way the verdict goes below.
	// m.logger already carries user_id (bound in NewManager).
	for _, c := range corruptRows {
		m.logger.Warn("resume hit a corrupt journal row; falling back to a full snapshot",
			"batch_id", c.BatchID,
			"field", c.Field,
			"error", c.Cause)
	}
	// ErrDeltaTooLarge and ErrResumeCorrupt are both "cannot ship a delta, but a
	// full snapshot is fine" verdicts. Corrupt specifically MUST NOT degrade to
	// a partial delta: the frames after the bad row would advance the client's
	// max_hlc past a batch it never received, and ListBatchesAfter is
	// strictly-greater, so no later resume would re-request it — the divergence
	// would be permanent (and, since #356, written into the client's persisted
	// checkpoint). A snapshot is always complete.
	if errors.Is(tailErr, ErrDeltaTooLarge) || errors.Is(tailErr, ErrResumeCorrupt) {
		// Signal FALLBACK to the caller, which owns re-entry. Tear down first
		// so resumeFallback's re-register does not double-register.
		unsub()
		return resumeScanResult{kind: resumeScanFallback}
	}
	if tailErr != nil {
		unsub()
		return resumeScanResult{kind: resumeScanError, err: fmt.Errorf("resume: list batches after cursor: %w", tailErr)}
	}
	// Compaction (or an epoch bump) during the unlocked scan can DeleteThrough
	// journal rows the verdict believed were still available — ListBatchesAfter
	// then returns a shortened/empty tail without ErrDeltaTooLarge, and the
	// client would adopt max_hlc while missing compacted-away ops. Re-check the
	// resumable predicate against live state; if the cursor is no longer above
	// the watermark (or the epoch moved), signal FALLBACK.
	if !m.decideResume(req.cursor, req.clientEpoch) {
		unsub()
		return resumeScanResult{kind: resumeScanFallback}
	}
	// The transport could not buffer a live frame while this scan ran, so the
	// delta it is about to ship would be missing whatever it dropped. FALLBACK
	// rather than shipping a hole -- and rather than letting the transport tear
	// the connection down, which sent the client back with the same cursor to
	// rebuild the same multi-page scan under the same load.
	//
	// Checked HERE, after the scan and before any frame is built, because
	// resumeFallback re-registers and calls OnRebaseline -- which discards the
	// parked buffer -- before taking the snapshot baseline. A frame dropped
	// during the scan is therefore superseded by that snapshot, not lost.
	if req.sub.overflowed() {
		m.logger.Warn("resume subscriber overflowed its park buffer during the scan; falling back to a snapshot",
			"client_id", req.sub.ClientID)
		unsub()
		return resumeScanResult{kind: resumeScanFallback}
	}

	// Build the ordered resume frame stream via the SAME emitCatchUpFrames
	// planner the live broadcast uses. Frames are WatchUserEvent values
	// (entity_materialized | batch | entity_removed | batch_end) — the unified envelope
	// live Send already speaks — so the client dispatches resume and live
	// through one switch. No manager lock across the frame build: snapshots
	// are persisted. The only live-state access is the epoch read below.
	//
	// ONE sink for the whole tail, constructed here rather than per batch: it
	// OWNS both accumulators (the frame stream and the max_hlc those frames
	// advertise), so re-boxing it inside the loop would only re-wrap the same
	// two fields once per batch.
	sink := &resumeCatchUpSink{
		frames: make([]*leapmuxv1.WatchUserEvent, 0, len(tail)),
		maxHLC: HLCClone(req.cursor),
	}
	// Restore the pre-side of each transition to its CURSOR-ERA answer. Without
	// this the replay evaluates gap batches against the ACL as it stands now, so
	// a workspace deleted during the gap reads as invisible on both sides of its
	// own tombstone batch and the client is never told to drop it. See
	// visibilityScope.
	scope := resumeScope(req.filter, m.departedWorkspaces(req.filter, req.sub.RequestedWorkspaceIDs, tail))
	for _, rb := range tail {
		transitions, records := TransitionsFromProto(rb.Transitions)
		atHLC := lastBatchHLC(rb.Batch)
		cb := catchUpBatch{
			refs:        orderedAffectedRefs(transitions),
			batch:       rb.Batch,
			transitions: transitions,
			atHLC:       atHLC,
		}
		emitCatchUpFrames(cb, scope, func(ref EntityRef) *leapmuxv1.EntityMaterialized {
			rec, ok := records[ref]
			if !ok || rec == nil {
				return nil
			}
			// Fresh AtHlc stamp — do not mutate the records map entry.
			return &leapmuxv1.EntityMaterialized{AtHlc: atHLC, Entity: rec.Entity}
		}, sink)
		// Carry this batch's outcome into the next one's pre side. Must run
		// AFTER the whole batch is classified, never between its passes.
		scope.observe(cb.refs)
	}
	m.mu.RLock()
	epoch := m.state.GetCurrentEpoch()
	m.mu.RUnlock()

	delta := &leapmuxv1.ResumeDelta{
		Frames:       sink.frames,
		MaxHlc:       sink.maxHLC,
		CurrentEpoch: epoch,
	}
	// EXACT frame gate on the BUILT delta, an ADDITIONAL check beside the scan's
	// MaxResumeDeltaBytes source-row budget rather than a replacement for it:
	// that one bounds MEMORY during a streaming scan, before any delta exists to
	// measure, while this one bounds what actually goes on the WIRE. The whole
	// delta ships as ONE WebSocket message (SubscribeOutcome.Bootstrap wraps it
	// into a single MarshaledEvent), so exceeding the client's read limit is not
	// a slow path -- the read fails, the client reconnects with the SAME cursor,
	// rebuilds the SAME oversized frame, and loops forever with no server-side
	// error to attribute it to. FALLBACK instead: a snapshot of the same account
	// is smaller by construction here, because the delta's bulk is the per-batch
	// record snapshots in transitions_payload (one copy per batch that touched an
	// entity) where the materialized state carries each entity exactly once.
	if size := proto.Size(delta); size > m.maxResumeDeltaFrameBytes {
		m.logger.Warn("resume delta exceeds the wire frame ceiling; falling back to a full snapshot",
			"delta_bytes", size,
			"ceiling_bytes", m.maxResumeDeltaFrameBytes,
			"frames", len(sink.frames))
		// Same teardown as the over-budget scan arm: the caller owns FALLBACK
		// re-entry and re-registers from scratch.
		unsub()
		return resumeScanResult{kind: resumeScanFallback}
	}
	return resumeScanResult{kind: resumeScanDelta, outcome: newSubscribeDeltaOutcome(delta, unsub)}
}

// departedWorkspaces names the workspaces a resuming subscriber could see when
// its cursor was minted but cannot see now, so buildResumeDelta can evaluate the
// PRE side of each gap transition against the cursor-era ACL.
//
// It is derived from state rather than from an ACL snapshot, because the CRDT
// already records the only event that removes a workspace from an owner's set:
// TombstoneWorkspaceOp drops the WorkspaceContentsRecord. Access is owner-only
// (there is no sharing to revoke and no cross-owner grant), so within one user's
// manager "referenced as a transition's Pre, not currently allowed, and no
// longer present in state" means exactly "deleted since". A workspace the
// subscriber merely narrowed away with workspace_ids is still IN state, so it is
// correctly not treated as departed.
//
// Returns nil when nothing departed, which makes visibilityScope.preVisible a
// plain filter lookup on the overwhelmingly common path.
//
// Takes the filter BY VALUE rather than reading sub.Filter: buildResumeDelta
// runs with neither subscribeExpandMu nor the projection lock held (that is the
// whole point of the lock-free tail scan), while contractSubscribersForWorkspace
// assigns sub.Filter under the SubscriberController's own mutex on every
// workspace delete. Reading the live field here would be an unsynchronized read
// racing that write, and would answer against a different filter than the
// visibilityScope built from req.filter two lines up. requested is
// Subscriber.RequestedWorkspaceIDs, which is immutable after Add and so is safe
// to hand over directly.
func (m *Manager) departedWorkspaces(filter SubscriberFilter, requested map[string]bool, tail []ResumeBatch) map[string]bool {
	var candidates map[string]bool
	for _, rb := range tail {
		for _, entry := range rb.Transitions.GetEntries() {
			pre := entry.GetPreWorkspace()
			if pre == "" || filter.IsAllowed(pre) {
				continue
			}
			// An explicitly narrowed subscriber never held workspaces outside
			// its request, so evicting them would be noise.
			if len(requested) > 0 && !requested[pre] {
				continue
			}
			if candidates == nil {
				candidates = map[string]bool{}
			}
			candidates[pre] = true
		}
	}
	if candidates == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	live := m.state.GetWorkspaces()
	for ws := range candidates {
		if _, stillExists := live[ws]; stillExists {
			// Present in state but outside the filter: a workspace the caller
			// narrowed away, not one that was deleted.
			delete(candidates, ws)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// resumeFallback re-enters the FALLBACK seam (full snapshot) after a resume
// scan gave up. buildResumeDelta has ALREADY torn down the RESUME registration
// on every path that reaches here, so this only re-registers.
//
// resolve runs again under subscribeExpandMu so the snapshot filter reflects
// the current ACL — the filter installed at the earlier resolve may have been
// expand/contract-mutated while the subscriber was registered, and a stale copy
// must not seed the FALLBACK baseline.
func (m *Manager) resumeFallback(
	sub *Subscriber,
	resolve func() (map[string]bool, error),
) (SubscribeOutcome, error) {
	// Same panic-safety reasoning as SubscribeWithACL: resolve() reaches the
	// store and a recovered handler panic must not leak either lock.
	m.subscribeExpandMu.Lock()
	defer m.subscribeExpandMu.Unlock()

	allowed, err := resolve()
	if err != nil {
		return SubscribeOutcome{}, err
	}
	sub.Filter = NewSubscriberFilter(allowed)
	// Clear the resume suppress gate: FALLBACK re-registers fresh and must
	// receive every live broadcast after the snapshot baseline.
	sub.resumeSuppressThrough = nil
	m.projection.Lock()
	defer m.projection.Unlock()
	// Discard anything the transport queued during the resume scan BEFORE the
	// baseline is taken. Those frames predate the snapshot, and the client
	// applies entity_materialized / entity_removed wholesale, so writing them
	// after it would resurrect stale records permanently. Both calls sit under
	// m.projection, so no broadcast can slip between the discard and the clone.
	if sub.OnRebaseline != nil {
		sub.OnRebaseline()
	}
	return m.fallbackOutcome(sub), nil
}

// SubscribeMode discriminates which bootstrap frame SubscribeOutcome carries.
type SubscribeMode int

const (
	// subscribeModeInvalid is the ZERO VALUE, and is deliberately not a real
	// arm. Every error return is `SubscribeOutcome{}`, so without this the zero
	// outcome would read as "SubscribeDelta, ready" and Delta() would hand back
	// a nil *ResumeDelta -- a caller that logged Mode() before checking err, or
	// a second caller that treated a non-error-checked outcome as usable, would
	// ship WatchUserEvent_Delta{Delta: nil} as the bootstrap frame and leave the
	// client "connected" against unhydrated state with every submit rejected
	// STALE_EPOCH. This mirrors resumeScanInvalid on the sibling
	// resumeScanKind, which was given the same guard for the same reason.
	subscribeModeInvalid SubscribeMode = iota
	// SubscribeDelta means the hub honored the client's cursor and shipped the
	// post-cursor op tail as a WatchUserEvent_Delta.
	SubscribeDelta
	// SubscribeInitial means the hub could not resume (cursor nil/zero, at/below
	// op_retention_watermark, stale epoch, tail over budget, or a built delta
	// over the wire-frame ceiling) and shipped a full UserMaterialized snapshot
	// as a WatchUserEvent_Initial.
	//
	// NOT compaction_watermark: those two deliberately diverge by OpRetentionTTL
	// (see laggedRetentionWatermark), and compaction_watermark always equals
	// max_hlc, so reading this gate as the tight one makes every cursor look
	// stale. decideResume is the authority.
	SubscribeInitial
)

// String names the arm so a wrong-access panic (Delta()/Initial() on the
// non-matching arm) reports the arm textually instead of an opaque integer.
func (m SubscribeMode) String() string {
	switch m {
	case subscribeModeInvalid:
		return "SubscribeMode(invalid)"
	case SubscribeDelta:
		return "SubscribeDelta"
	case SubscribeInitial:
		return "SubscribeInitial"
	default:
		return fmt.Sprintf("SubscribeMode(%d)", int(m))
	}
}

// SubscribeOutcome is the discriminated result of SubscribeWithACL. Exactly one
// bootstrap arm is selected (Mode), and its payload is the only non-nil one —
// the constructors enforce the one-payload invariant, so the caller cannot
// construct an outcome where both arms (or neither) are populated. The caller
// reads Mode and calls Bootstrap() for the matching frame, or Unsub() for the
// unsubscribe handle. This replaces the prior (delta, fellBack, unsub, err)
// nil-discipline whose "exactly one of delta/fellBack is non-nil on success"
// contract lived in prose rather than the type.
type SubscribeOutcome struct {
	mode    SubscribeMode
	delta   *leapmuxv1.ResumeDelta
	initial *leapmuxv1.UserMaterialized
	unsub   func()
}

// Mode reports which bootstrap arm this outcome selected.
func (o SubscribeOutcome) Mode() SubscribeMode { return o.mode }

// Unsub returns the idempotent unsubscribe handle for the registered
// subscriber. The caller MUST defer it on a non-error outcome (the error path
// tears down the registration before returning, leaving no handle to defer).
// The nil-guard makes that contract mechanical: a caller that defers Unsub
// before checking err gets a no-op rather than a nil-function panic.
func (o SubscribeOutcome) Unsub() func() {
	if o.unsub == nil {
		return func() {}
	}
	return o.unsub
}

// Delta returns the ResumeDelta payload. Panics unless Mode() == SubscribeDelta —
// the discriminated access is intentionally assertion-gated so a caller that
// reads the wrong arm's payload fails loudly instead of silently sending nil.
func (o SubscribeOutcome) Delta() *leapmuxv1.ResumeDelta {
	if o.mode != SubscribeDelta {
		panic(fmt.Sprintf("crdt: SubscribeOutcome.Delta called in %v mode", o.mode))
	}
	return o.delta
}

// Initial returns the full-snapshot payload. Panics unless Mode() ==
// SubscribeInitial, for the same reason as Delta().
func (o SubscribeOutcome) Initial() *leapmuxv1.UserMaterialized {
	if o.mode != SubscribeInitial {
		panic(fmt.Sprintf("crdt: SubscribeOutcome.Initial called in %v mode", o.mode))
	}
	return o.initial
}

// Bootstrap wraps whichever payload arm this outcome holds into the single
// WatchUserEvent the connection sends first, stamping the two identity fields
// every bootstrap frame must carry.
//
// BOTH arms stamp subscriber_client_id: it is the frontend active-client gate's
// only source of its own identity, and since the client-checkpoint work a
// resume is the normal COLD-START path -- a refreshed page hydrates from
// IndexedDB and resumes, so there is no earlier `initial` frame it could have
// learned the id from. Both stamp user_id so the client can fail closed on a
// foreign payload the way it already does for its persisted checkpoint. (The
// manager already names the tenant on the initial arm with this same value;
// writing it here too keeps ONE stamping rule at the seam instead of one per
// arm, which is how the arms drifted in the first place.)
//
// Returning the wrapped event from the type is what makes "exactly one
// bootstrap frame goes on the wire" a property of SubscribeOutcome rather than
// of a correctly-written if/else at the single call site. Panics on an invalid
// mode, for the same reason Delta()/Initial() do.
func (o SubscribeOutcome) Bootstrap(subscriberClientID, userID string) *leapmuxv1.WatchUserEvent {
	switch o.mode {
	case SubscribeDelta:
		o.delta.SubscriberClientId = subscriberClientID
		o.delta.UserId = userID
		return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Delta{Delta: o.delta}}
	case SubscribeInitial:
		o.initial.SubscriberClientId = subscriberClientID
		o.initial.UserId = userID
		return &leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Initial{Initial: o.initial}}
	default:
		panic(fmt.Sprintf("crdt: SubscribeOutcome.Bootstrap called in %v mode", o.mode))
	}
}

func newSubscribeDeltaOutcome(delta *leapmuxv1.ResumeDelta, unsub func()) SubscribeOutcome {
	return SubscribeOutcome{mode: SubscribeDelta, delta: delta, unsub: unsub}
}

func newSubscribeInitialOutcome(initial *leapmuxv1.UserMaterialized, unsub func()) SubscribeOutcome {
	return SubscribeOutcome{mode: SubscribeInitial, initial: initial, unsub: unsub}
}

// fallbackOutcome is the FALLBACK seam shared by SubscribeWithACL's two FALLBACK
// triggers (a non-resumable verdict and an over-budget tail): register + full
// materialized snapshot via subscribeLocked, wrapped in a SubscribeInitial
// outcome with a fresh idempotent unsub. MUST be called with m.projection held
// (subscribeLocked requires it). Folding the two identical call sites into one
// seam keeps the "FALLBACK always re-registers fresh via subscribeLocked"
// invariant in one readable expression.
func (m *Manager) fallbackOutcome(sub *Subscriber) SubscribeOutcome {
	initial := m.subscribeLocked(sub)
	return newSubscribeInitialOutcome(initial, makeUnsub(sub, m))
}

// Subscribe attaches a new subscriber. Returns an unsubscribe
// callback. Bootstrap is sent inline (the caller's stream layer
// formats it).
//
// TEST-ONLY as of the resume extraction: `rg '\.Subscribe\(' --glob '!*_test.go'`
// finds no production caller. The userevents path goes through
// SubscribeWithACL, which reaches the SAME registerSubscriber/makeUnsub seam
// via subscribeLocked and registerForResume while holding subscribeExpandMu, so
// a concurrent workspace-create expansion cannot leave a straggler with a stale
// pre-commit filter (see SubscribeWithACL). This wrapper only adds the
// projection lock around that seam and does NOT get the subscribeExpandMu
// guarantee -- so a fix made here alone does not reach production.
//
// Subscribers with a non-empty ClientID contribute to a refcount keyed
// on that id. The first Subscribe cancels any pending deferred clear;
// the last unsub schedules one PresenceClearGrace into the future. A
// reconnect inside the grace window keeps the client's presence
// entries intact so the active-client gate doesn't flicker.
func (m *Manager) Subscribe(sub *Subscriber) (initial *leapmuxv1.UserMaterialized, unsub func()) {
	// The whole sequence runs under m.projection -- the same lock broadcasts
	// take -- so a subscriber's registration + initial snapshot are atomic with
	// respect to any concurrent broadcast: the filter captured by Add and the
	// materialized baseline can never straddle a filter mutation. (The
	// production path must go through SubscribeWithACL rather than resolving a
	// filter and Adding it here unserialized -- a workspace-create expansion
	// only visits already-registered subscribers; see SubscribeWithACL.) The
	// cost is that the O(N) materializedLocked clone below is serialized
	// against broadcasts for that duration (a large-user-doc connect briefly stalls
	// the user's commit/broadcast pipeline); relaxing it -- computing the
	// snapshot outside m.projection -- would reopen exactly that straddle
	// window, so it is held deliberately.
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
	initial = m.subscribeLocked(sub)
	m.projection.Unlock()
	return initial, makeUnsub(sub, m)
}

// subscribeLocked is the register+snapshot core shared by Subscribe and
// SubscribeWithACL's FALLBACK path. It MUST be called with m.projection held; it
// takes m.mu.RLock for the materialized clone itself. It registers the
// subscriber (Add + presence refcount) and returns the full UserMaterialized
// baseline. SubscribeWithACL's RESUME path does NOT call this -- it calls only
// registerSubscriber (to skip the materialized clone it does not need) and
// builds a ResumeDelta instead of the snapshot.
func (m *Manager) subscribeLocked(sub *Subscriber) *leapmuxv1.UserMaterialized {
	initialFilter := m.registerSubscriber(sub)
	m.mu.RLock()
	initial := m.materializedLocked(initialFilter)
	m.mu.RUnlock()
	return initial
}

// registerSubscriber is the register half of subscribeLocked: Add + presence
// refcount. Split out so SubscribeWithACL's RESUME path registers through the SAME
// seam as Subscribe/FALLBACK instead of hand-mirroring the Add+OnConnect pair,
// keeping the presence-refcount invariant in one place. The matching teardown
// is makeUnsub (idempotent) — call it on any error path between a successful
// register and the return of a makeUnsub the caller will defer.
func (m *Manager) registerSubscriber(sub *Subscriber) SubscriberFilter {
	initialFilter := m.subscribers.Add(sub)
	m.presenceCtl.OnConnect(sub.ClientID)
	return initialFilter
}

// makeUnsub returns the idempotent unsubscribe closure for a registered
// subscriber. Idempotent: a caller that invokes it twice (an error-path
// cleanup racing a deferred cleanup, or the partway-through-unsubscribe
// teardown the registry hardens against) must not decrement the presence
// refcount twice -- that would underflow the count and prematurely clear a
// client that still has live connections, flickering the active-client gate.
// It is also the teardown registerForResume+error paths reach for directly
// (before re-entering subscribeLocked on a FALLBACK), since calling it twice is
// safe.
func makeUnsub(sub *Subscriber, m *Manager) func() {
	var unsubOnce sync.Once
	return func() {
		unsubOnce.Do(func() {
			m.subscribers.Remove(sub)
			m.presenceCtl.OnDisconnect(sub.ClientID)
		})
	}
}
