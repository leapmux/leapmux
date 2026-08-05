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
// more than one is held (both arms hold subscribeExpandMu across a register
// window that itself takes projection and then m.mu).
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
// as the `initial` frame). SubscribeOutcome.Reason() names which of those fired.
//
// The FALLBACK arm holds NO manager lock across its O(all-entities) baseline
// either. registerForFallback takes m.projection and m.mu.RLock for an O(1)
// window -- register the subscriber, capture the published generation, set
// resumeSuppressThrough from that generation's max_hlc -- and releases both;
// materializedFromState then walks the captured generation, which is immutable
// (see commitState), holding nothing. registerForFallback's doc carries the
// no-gap / no-duplicate proof.
//
// The one exception is the park-overflow ladder's terminal rung
// (lockedFallbackOutcome), which holds m.projection across its walk on purpose:
// that is what makes overflow impossible there and the ladder finite. It is
// reached only after fallbackLockFreeAttempts consecutive overflows.
//
// subscribeExpandMu is likewise released after the register, not held across
// the baseline. A concurrent lifecycle op can then mutate sub.Filter while the
// baseline is being built from the Add-captured copy, and both directions are
// still correct:
//
//   - CREATE expands filters BEFORE broadcasting the seed batch
//     (applyLifecycleCreate), and we registered first, so the expand sees us.
//     The seed batch's HLC is above the captured generation's max_hlc, so it
//     arrives live. The baseline omits the new workspace, which is right --
//     it had no pre-existing entities.
//   - DELETE submits and broadcasts the tombstone batch and only then contracts
//     (applyLifecycleDelete). A tombstone below the baseline is already
//     reflected in it; one above it is broadcast to us.
//
// SubscriberFilter's replace-don't-mutate discipline plus cloneSubscriber's
// deep copy make the captured filter a private, race-free value -- the same
// argument the RESUME arm already relies on.
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
	// register window, then RELEASED before the expensive part of EITHER arm --
	// the RESUME tail read and the FALLBACK baseline build -- so neither stalls
	// that user's workspace-create/delete lifecycle RPCs.
	//
	// m.projection is likewise held only across a brief register window on both
	// arms: the until-capture+register on RESUME, and the
	// generation-capture+register on FALLBACK (plus, on a rebaseline, the
	// discard of the superseded parked frames). Neither the tail read nor the
	// baseline build holds it -- except on the park-overflow ladder's terminal
	// rung, which holds m.projection deliberately; see fallbackOutcome. On both
	// arms the straddle is closed by
	// REGISTRATION ORDER plus resumeSuppressThrough rather than by holding the
	// lock, which is what keeps a connect from stalling every other tab's live
	// commit broadcasts for its duration.
	// The RESUME arm's locked section below is a closure so its unlock runs via
	// defer (the FALLBACK arm's lives in resolveAndRegisterForFallback, which
	// does the same). resolve() reaches the store, and net/http RECOVERS a
	// handler panic and keeps the process serving -- so a panic under
	// subscribeExpandMu would leak it and wedge every later /ws/userevents
	// connect plus every workspace create/delete for this user, silently and
	// until restart.
	//
	// The verdict reads only m.state, so it needs neither subscribeExpandMu nor
	// the resolved filter. Deciding first lets the FALLBACK arm delegate the
	// whole lock dance -- resolve, register, release, build -- to fallbackOutcome,
	// which is also where the RESUME->FALLBACK re-entry lands, so both fallbacks
	// go through one seam with one lock plan instead of two.
	if resume, reason := m.decideResume(cursor, clientEpoch); !resume {
		return m.fallbackOutcome(sub, resolve, fallbackCold, reason)
	}

	var reg registration
	if err := func() error {
		m.subscribeExpandMu.Lock()
		defer m.subscribeExpandMu.Unlock()

		// Through the same helper both FALLBACK rungs use, so all THREE
		// resolve-then-install sites have one spelling and the "caller MUST hold
		// subscribeExpandMu" contract has one home.
		if err := m.resolveFilter(sub, resolve); err != nil {
			return err
		}

		// RESUME: register the subscriber via the same seam the FALLBACK arm
		// uses (so the presence-refcount invariant has one home), then build the
		// delta. The FALLBACK seam would also compute a materialized baseline
		// the resume does not need, so call only its register half.
		// subscribeExpandMu is still held so the register stays serialized
		// against a concurrent expand; it is released on return from this
		// closure, before buildResumeDelta's DB scan.
		reg = m.registerForResume(sub)
		return nil
	}(); err != nil {
		return SubscribeOutcome{}, err
	}

	// The register window owns the unsub handle and hands it over (makeUnsub is
	// idempotent, so it doubles as teardown for the FALLBACK re-entry the caller
	// performs). buildResumeDelta returns a resumeScanResult; the caller (this
	// function) owns the FALLBACK re-entry, so buildResumeDelta no longer takes
	// the resolve/unsub teardown callbacks.
	req := resumeRequest{
		sub:         sub,
		filter:      reg.filter,
		cursor:      cursor,
		until:       reg.maxHLC,
		clientEpoch: clientEpoch,
		unsub:       reg.unsub,
	}
	res := m.buildResumeDelta(ctx, req)
	switch res.kind {
	case resumeScanDelta:
		return res.outcome, nil
	case resumeScanFallback:
		// buildResumeDelta signaled FALLBACK (over-budget / corrupt / post-scan
		// compaction-or-epoch drift). It already tore down the registration;
		// re-enter the FALLBACK seam, which re-resolves + re-registers fresh.
		return m.fallbackOutcome(sub, resolve, fallbackRebaseline, res.reason)
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

// decideResume is the RESUME-vs-FALLBACK verdict against live state, plus the
// REASON it said no: the cursor
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
//
// The reason exists because the predicate used to be one collapsed boolean,
// which meant the production answer to "why is this deployment
// full-snapshotting?" was unobtainable: a no-cursor first connect, a client
// dormant past the retention floor and a stale epoch are three different
// operational stories with three different fixes, and they were
// indistinguishable. It rides on SubscribeOutcome to the service layer, which
// labels the metric. The verdict itself is unchanged.
//
// This name is the codebase's canonical term for the predicate — it is cited by
// name from the journal wire format, the op-batch SQL, the retention sweep and
// the frontend's pendingOps/checkpointStore — so it stays attached to the code
// that actually implements it rather than to a wrapper.
func (m *Manager) decideResume(cursor *leapmuxv1.HLC, clientEpoch int64) (bool, SubscribeReason) {
	if cursor == nil || HLCIsZero(cursor) {
		return false, SubscribeReasonNoCursor
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
	if HLCCmp(cursor, m.opRetentionFloorLocked()) <= 0 {
		return false, SubscribeReasonBelowRetentionFloor
	}
	if clientEpoch != m.state.GetCurrentEpoch() {
		return false, SubscribeReasonStaleEpoch
	}
	return true, SubscribeReasonResumed
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
func (m *Manager) registerForResume(sub *Subscriber) registration {
	m.projection.Lock()
	defer m.projection.Unlock()
	return m.registerLocked(sub)
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
	// unsub is the register window's handle, carried rather than re-minted.
	// makeUnsub returns a FRESH sync.Once per call, so two handles over one
	// subscriber would each be free to decrement the presence refcount -- the
	// underflow makeUnsub's own idempotence exists to prevent, reintroduced by
	// having two of them.
	unsub func()
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
	kind resumeScanKind
	// reason is set on resumeScanFallback and names which of the four post-scan
	// give-ups fired, so the caller can label the FALLBACK it re-enters. Unset
	// on the other kinds, which carry their verdict in `outcome` / `err`.
	//
	// Always populate it via newResumeScanFallback, never by struct literal.
	reason  SubscribeReason
	outcome SubscribeOutcome
	err     error
}

// newResumeScanFallback is the ONLY way to build a resumeScanFallback result.
//
// It exists because the reason is the whole point of the enum and a struct
// literal makes it optional: an arm that omitted `reason:` compiled fine and
// shipped the zero subscribeReasonInvalid all the way to
// leapmux_userevents_subscribe_total{reason="invalid"} — an unnamed bucket for
// the exact defect class the label was added to surface, indistinguishable from
// a construction bug. Requiring it as an ARGUMENT makes that omission a compile
// error at every one of the four give-up arms instead of a silent mislabel.
// (newSubscribeInitialOutcome takes its reason the same way, for the same
// reason.)
func newResumeScanFallback(reason SubscribeReason) resumeScanResult {
	return resumeScanResult{kind: resumeScanFallback, reason: reason}
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
// re-resolves and re-registers from scratch through fallbackOutcome); on a hard
// tail-read error it tears down + returns the error; on success it returns the
// delta outcome.
func (m *Manager) buildResumeDelta(ctx context.Context, req resumeRequest) resumeScanResult {
	unsub := req.unsub
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
		// so the fallback seam's re-register does not double-register.
		reason := SubscribeReasonTailOverBudget
		if errors.Is(tailErr, ErrResumeCorrupt) {
			reason = SubscribeReasonCorruptRow
		}
		unsub()
		return newResumeScanFallback(reason)
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
	if resume, drifted := m.decideResume(req.cursor, req.clientEpoch); !resume {
		// The LABEL stays post_scan_drift -- "the verdict was invalidated during
		// the unlocked scan" is a different operational story from a verdict
		// that was already stale when it was taken, and collapsing them would
		// lose that. But WHICH way it drifted (a compaction ate the tail, or the
		// epoch bumped) has different fixes, so it is logged rather than
		// discarded. This is also the only give-up arm that logged nothing.
		m.logger.Warn("resume verdict drifted during the tail scan; falling back to a snapshot",
			"drifted_to", drifted, "client_id", req.sub.ClientID)
		unsub()
		return newResumeScanFallback(SubscribeReasonPostScanDrift)
	}
	// The transport could not buffer a live frame while this scan ran, so the
	// delta it is about to ship would be missing whatever it dropped. FALLBACK
	// rather than shipping a hole -- and rather than letting the transport tear
	// the connection down, which sent the client back with the same cursor to
	// rebuild the same multi-page scan under the same load.
	//
	// Checked HERE, after the scan and before any frame is built, because
	// the fallback seam re-registers and calls OnRebaseline -- which discards the
	// parked buffer -- before taking the snapshot baseline. A frame dropped
	// during the scan is therefore superseded by that snapshot, not lost.
	//
	// The reason must be stated HERE and cannot be re-derived downstream:
	// fallbackOutcome's own overflow re-check runs AFTER registerForFallback has
	// called OnRebaseline, and the real transport's Rebaseline clears the flag —
	// so by the time that check runs, the drop this arm observed is invisible.
	if req.sub.overflowed() {
		m.logger.Warn("resume subscriber overflowed its park buffer during the scan; falling back to a snapshot",
			"client_id", req.sub.ClientID)
		unsub()
		return newResumeScanFallback(SubscribeReasonParkOverflow)
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
		return newResumeScanFallback(SubscribeReasonDeltaOverFrameCeiling)
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

// resolveAndRegisterForFallback is one register window of the FALLBACK seam:
// resolve the ACL and register, atomically with respect to a concurrent
// ExpandSubscribersForWorkspace, then RELEASE subscribeExpandMu so the caller
// can build the baseline without it.
//
// resolve runs on EVERY attempt, not once: the filter installed by an earlier
// resolve may have been expand/contract-mutated while the subscriber was
// registered, and a stale copy must not seed the baseline. It is also what
// keeps the resolve→register TOCTOU closed on a ladder RETRY, which unregisters
// and re-registers -- a window an expand would otherwise slip through.
//
// The lock is taken here rather than by the caller so the whole loop in
// fallbackOutcome cannot accidentally hold it across a baseline: there is no
// call path that holds subscribeExpandMu on entry.
func (m *Manager) resolveAndRegisterForFallback(
	sub *Subscriber,
	resolve func() (map[string]bool, error),
	entry fallbackEntry,
) (registration, error) {
	// Same panic-safety reasoning as SubscribeWithACL: resolve() reaches the
	// store and a recovered handler panic must not leak the lock.
	m.subscribeExpandMu.Lock()
	defer m.subscribeExpandMu.Unlock()

	if err := m.resolveFilter(sub, resolve); err != nil {
		return registration{}, err
	}
	return m.registerForFallback(sub, entry), nil
}

// resolveFilter re-resolves the ACL and installs it on sub.
//
// Caller MUST hold subscribeExpandMu — that is what makes resolve→register
// atomic against a concurrent ExpandSubscribersForWorkspace. Split out so all
// three resolve-then-register sites -- the RESUME arm and the fallback ladder's
// lock-free and escalated rungs -- share one spelling of the pair.
func (m *Manager) resolveFilter(sub *Subscriber, resolve func() (map[string]bool, error)) error {
	allowed, err := resolve()
	if err != nil {
		return err
	}
	sub.Filter = NewSubscriberFilter(allowed)
	return nil
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

// SubscribeReason says WHY a connect got the bootstrap arm it did.
//
// Mode alone answers "snapshot or delta"; this answers "and why", which is the
// question an operator actually has. The two are carried together on
// SubscribeOutcome so the service layer can label one metric with both without
// re-deriving anything, and so a fallback whose cause is a corrupt journal row
// is distinguishable from one whose cause is a client that was simply away for
// two days.
type SubscribeReason int

const (
	// subscribeReasonInvalid is the ZERO VALUE and deliberately not a real
	// reason, for the same purpose as subscribeModeInvalid and
	// resumeScanInvalid: a construction path that forgets to set the reason
	// reports "invalid" rather than silently claiming the first real arm.
	subscribeReasonInvalid SubscribeReason = iota
	// SubscribeReasonResumed: the cursor was honored and a delta shipped.
	SubscribeReasonResumed
	// SubscribeReasonNoCursor: no cursor presented (a first-ever connect, or a
	// client whose confirmed state is empty so its cursor would be meaningless).
	SubscribeReasonNoCursor
	// SubscribeReasonBelowRetentionFloor: the cursor sits at or below
	// op_retention_watermark, so the journal can no longer reconstruct the
	// delta. The dormant-client case.
	SubscribeReasonBelowRetentionFloor
	// SubscribeReasonStaleEpoch: the client's epoch no longer matches the hub's.
	SubscribeReasonStaleEpoch
	// SubscribeReasonTailOverBudget: the post-cursor tail exceeded
	// MaxResumeDeltaOps / MaxResumeDeltaBytes.
	SubscribeReasonTailOverBudget
	// SubscribeReasonDeltaOverFrameCeiling: the BUILT delta exceeded
	// MaxResumeDeltaFrameBytes, the wire-frame gate.
	SubscribeReasonDeltaOverFrameCeiling
	// SubscribeReasonCorruptRow: a journal row in the tail failed to decode.
	SubscribeReasonCorruptRow
	// SubscribeReasonPostScanDrift: a compaction or epoch bump landed during
	// the (lock-free) tail scan and invalidated the verdict.
	SubscribeReasonPostScanDrift
	// SubscribeReasonParkOverflow: the transport dropped a frame parked during
	// the resume scan, so the delta would have shipped over a hole.
	SubscribeReasonParkOverflow

	// subscribeReasonMax is a NON-REASON sentinel and MUST stay last. Add new
	// reasons above it.
	//
	// It exists so the vocabulary test can walk every declared member without
	// restating whichever one is currently last. Bounding that walk by the last
	// real member made it blind to the normal way the enum grows: a reason
	// APPENDED after the bound was never visited, so forgetting its String() arm
	// left the test green and shipped "SubscribeReason(10)" as a Prometheus
	// label value that no dashboard matches.
	subscribeReasonMax
)

// Label is the metric-label spelling of the reason: a stable, lowercase,
// snake_case vocabulary. These ARE Prometheus label values, so renaming one
// breaks dashboards.
//
// Separate from String() for the same reason SubscribeMode.Label() is, and the
// separation matters more here. String() is what %v and slog reach for
// implicitly, so while the vocabulary lived on String() a purely cosmetic edit
// to how a reason PRINTS would silently rename a dashboard label -- exactly the
// coupling SubscribeMode.Label() was introduced to break, left in place on the
// enum with eight arms and the one that will grow.
func (r SubscribeReason) Label() string {
	switch r {
	case subscribeReasonInvalid:
		return "invalid"
	case SubscribeReasonResumed:
		return "resumed"
	case SubscribeReasonNoCursor:
		return "no_cursor"
	case SubscribeReasonBelowRetentionFloor:
		return "below_retention_floor"
	case SubscribeReasonStaleEpoch:
		return "stale_epoch"
	case SubscribeReasonTailOverBudget:
		return "tail_over_budget"
	case SubscribeReasonDeltaOverFrameCeiling:
		return "delta_over_frame_ceiling"
	case SubscribeReasonCorruptRow:
		return "corrupt_row"
	case SubscribeReasonPostScanDrift:
		return "post_scan_drift"
	case SubscribeReasonParkOverflow:
		return "park_overflow"
	default:
		return fmt.Sprintf("reason_%d", int(r))
	}
}

// String names the Go constant, so a log line or a %v reads
// "SubscribeReasonNoCursor" beside "SubscribeInitial" rather than mixing a
// metric vocabulary into Go-facing text. One rule across both enums: String()
// is Go-facing, Label() is metric-facing.
func (r SubscribeReason) String() string {
	switch r {
	case subscribeReasonInvalid:
		return "SubscribeReason(invalid)"
	case SubscribeReasonResumed:
		return "SubscribeReasonResumed"
	case SubscribeReasonNoCursor:
		return "SubscribeReasonNoCursor"
	case SubscribeReasonBelowRetentionFloor:
		return "SubscribeReasonBelowRetentionFloor"
	case SubscribeReasonStaleEpoch:
		return "SubscribeReasonStaleEpoch"
	case SubscribeReasonTailOverBudget:
		return "SubscribeReasonTailOverBudget"
	case SubscribeReasonDeltaOverFrameCeiling:
		return "SubscribeReasonDeltaOverFrameCeiling"
	case SubscribeReasonCorruptRow:
		return "SubscribeReasonCorruptRow"
	case SubscribeReasonPostScanDrift:
		return "SubscribeReasonPostScanDrift"
	case SubscribeReasonParkOverflow:
		return "SubscribeReasonParkOverflow"
	default:
		return fmt.Sprintf("SubscribeReason(%d)", int(r))
	}
}

// Label is the metric-label spelling of the arm, in the same lowercase
// vocabulary SubscribeReason.String() uses.
//
// Separate from String() on purpose: String() feeds the wrong-access panic
// messages, which read better naming the Go constant ("Delta() called in
// SubscribeInitial mode"), while this feeds a Prometheus label, where a Go
// identifier would be out of place next to reason="no_cursor". Renaming either
// one must not silently rename the other -- a label rename breaks dashboards.
func (m SubscribeMode) Label() string {
	switch m {
	case SubscribeDelta:
		return "delta"
	case SubscribeInitial:
		return "initial"
	case subscribeModeInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("mode_%d", int(m))
	}
}

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
	reason  SubscribeReason
	delta   *leapmuxv1.ResumeDelta
	initial *leapmuxv1.UserMaterialized
	unsub   func()
}

// Mode reports which bootstrap arm this outcome selected.
func (o SubscribeOutcome) Mode() SubscribeMode { return o.mode }

// Reason reports WHY that arm was selected. Zero-valued (invalid) on the error
// outcomes, which carry no arm at all.
func (o SubscribeOutcome) Reason() SubscribeReason { return o.reason }

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
	return SubscribeOutcome{mode: SubscribeDelta, reason: SubscribeReasonResumed, delta: delta, unsub: unsub}
}

func newSubscribeInitialOutcome(initial *leapmuxv1.UserMaterialized, reason SubscribeReason, unsub func()) SubscribeOutcome {
	return SubscribeOutcome{mode: SubscribeInitial, reason: reason, initial: initial, unsub: unsub}
}

// fallbackLockFreeAttempts bounds the lock-free arm of fallbackOutcome before
// it escalates to the locked one. See fallbackOutcome for why the ladder
// terminates.
const fallbackLockFreeAttempts = 2

// FallbackLockFreeAttemptsForTest exposes the ladder's bound so the
// termination test asserts against the constant rather than restating it -- a
// restated 2 would silently stop testing the bound the day it changes.
const FallbackLockFreeAttemptsForTest = fallbackLockFreeAttempts

// fallbackOutcome is the FALLBACK seam shared by SubscribeWithACL's two
// triggers (a non-resumable verdict and an abandoned resume): register, build
// the full materialized baseline, and wrap it as a SubscribeInitial outcome.
//
// Caller MUST NOT hold subscribeExpandMu OR m.projection. This seam takes
// subscribeExpandMu itself, once per attempt, via resolveAndRegisterForFallback
// (and via lockedFallbackOutcome on the terminal rung) — sync.Mutex is not
// reentrant, so a caller that "helpfully" held it would deadlock this user's
// every later connect and workspace lifecycle RPC.
//
// THE PARK-OVERFLOW LADDER. The baseline is now built with the subscriber
// already registered and the transport still parking (Release happens only
// after the bootstrap frame is on the wire), so a slow build racing fast
// commits can overflow subscriberQueue's parkedFrameCap. That drops a frame,
// and liveCatchUpSink discards the send error -- so the frame is simply lost.
// It cannot happen today only because the old shape held m.projection across
// the build and stopped broadcasts entirely.
//
// So: retry on overflow via fallbackRebaseline, which discards the parked
// buffer (clearing the flag) and re-registers at a newer generation. After
// fallbackLockFreeAttempts, register and build INSIDE the m.projection hold --
// the pre-#267 shape, where no broadcast can interleave and overflow is
// therefore impossible. Termination is structural (a fixed count ending in an
// arm that cannot fail), not probabilistic.
func (m *Manager) fallbackOutcome(
	sub *Subscriber,
	resolve func() (map[string]bool, error),
	entry fallbackEntry,
	reason SubscribeReason,
) (SubscribeOutcome, error) {
	for attempt := range fallbackLockFreeAttempts {
		reg, err := m.resolveAndRegisterForFallback(sub, resolve, entry)
		if err != nil {
			// Returned UNREGISTERED, so the caller can reject the connection
			// before streaming anything.
			return SubscribeOutcome{}, err
		}
		initial := m.materialize(reg.state, reg.filter)
		if !sub.overflowed() {
			return newSubscribeInitialOutcome(initial, reason, reg.unsub), nil
		}
		// The overflow is now the operative cause of this snapshot, whatever
		// sent us here originally -- the retry below re-derives the baseline,
		// so attributing it to the earlier reason would hide the drop.
		reason = SubscribeReasonParkOverflow
		// The parked buffer has a hole, so this baseline cannot be shipped with
		// it. Tear down and retry; the next pass rebaselines, which drops the
		// buffer and clears the flag.
		reg.unsub()
		entry = fallbackRebaseline
		m.logger.Warn("userevents: park buffer overflowed during the fallback baseline; retrying",
			"attempt", attempt+1, "client_id", sub.ClientID)
	}
	// Last resort: hold m.projection across register + build so nothing can be
	// broadcast (and therefore nothing parked) while the baseline is taken.
	// This reinstates the BROADCAST stall this seam exists to remove, for this
	// one connect, which is strictly better than shipping a baseline with a hole.
	m.logger.Warn("userevents: fallback baseline escalating to the locked path after repeated park overflow",
		"client_id", sub.ClientID)
	return m.lockedFallbackOutcome(sub, resolve, entry, reason)
}

// lockedFallbackOutcome is the ladder's terminal rung: register AND build the
// baseline inside one m.projection hold, so no broadcast can interleave and the
// park buffer cannot overflow. That is what makes the ladder terminate.
//
// It holds m.projection across the O(all-entities) walk deliberately. It does
// NOT hold subscribeExpandMu across it: that lock covers only the
// resolve→register TOCTOU, and the lifecycle RPCs it serializes
// (ExpandSubscribersForWorkspace / contractSubscribersForWorkspace) take it
// without ever taking m.projection, so releasing it the moment the register
// window closes keeps this user's workspace create/delete off the walk while
// leaving the fence argument untouched. The acquisition order is still
// subscribeExpandMu → projection → m.mu; only the RELEASE is early.
func (m *Manager) lockedFallbackOutcome(
	sub *Subscriber,
	resolve func() (map[string]bool, error),
	entry fallbackEntry,
	reason SubscribeReason,
) (SubscribeOutcome, error) {
	// Idempotent so the deferred release is a panic-safety net rather than a
	// second unlock: resolve() reaches the store, and a recovered handler panic
	// must not leave this user's lifecycle lock held forever. sync.OnceFunc
	// rather than a hand-rolled bool-plus-closure -- "runs at most once" is that
	// primitive's entire contract, so it needs no reading to verify.
	m.subscribeExpandMu.Lock()
	releaseExpand := sync.OnceFunc(m.subscribeExpandMu.Unlock)
	defer releaseExpand()

	if err := m.resolveFilter(sub, resolve); err != nil {
		return SubscribeOutcome{}, err
	}
	m.projection.Lock()
	defer m.projection.Unlock()
	reg := m.registerForFallbackLocked(sub, entry)
	releaseExpand()
	return newSubscribeInitialOutcome(m.materialize(reg.state, reg.filter), reason, reg.unsub), nil
}

// Subscribe attaches a new subscriber. Returns an unsubscribe
// callback. Bootstrap is sent inline (the caller's stream layer
// formats it).
//
// TEST-ONLY as of the resume extraction: `rg '\.Subscribe\(' --glob '!*_test.go'`
// finds no production caller. The userevents path goes through
// SubscribeWithACL, which reaches the SAME registerSubscriber/makeUnsub seam
// via registerForFallback and registerForResume while holding subscribeExpandMu,
// so a concurrent workspace-create expansion cannot leave a straggler with a
// stale pre-commit filter (see SubscribeWithACL). This wrapper reaches
// registerForFallback WITHOUT that subscribeExpandMu guarantee -- so a fix made
// here alone does not reach production.
//
// Subscribers with a non-empty ClientID contribute to a refcount keyed
// on that id. The first Subscribe cancels any pending deferred clear;
// the last unsub schedules one PresenceClearGrace into the future. A
// reconnect inside the grace window keeps the client's presence
// entries intact so the active-client gate doesn't flicker.
func (m *Manager) Subscribe(sub *Subscriber) (initial *leapmuxv1.UserMaterialized, unsub func()) {
	// Reaches the same seam SubscribeWithACL's FALLBACK arm does, so the
	// register/baseline ordering has one home. fallbackCold because a fresh
	// subscriber has nothing parked to discard.
	//
	// (The production path must go through SubscribeWithACL rather than
	// resolving a filter and Adding it here unserialized -- a workspace-create
	// expansion only visits already-registered subscribers; see
	// SubscribeWithACL.)
	reg := m.registerForFallback(sub, fallbackCold)
	return m.materialize(reg.state, reg.filter), reg.unsub
}

// fallbackEntry says WHICH fallback a registration is, and both per-path
// answers derive from it: whether the transport has frames parked from a
// just-abandoned resume scan that the new baseline supersedes.
//
// An enum rather than a bool for the reason journalScan carries a scanMode:
// the two arms differ in what they must do, not merely in a flag's value.
type fallbackEntry int

const (
	// fallbackCold is a first bootstrap decision. The subscriber was never
	// registered, so nothing can be parked.
	fallbackCold fallbackEntry = iota
	// fallbackRebaseline is a RESUME that gave up. The subscriber WAS
	// registered during the (lock-free) journal scan, so live frames parked in
	// the transport; the new baseline supersedes them and they must be dropped
	// before it is taken.
	fallbackRebaseline
)

// registration is what the O(1) register window hands to the lock-free work
// that follows it -- a FALLBACK baseline build, or a RESUME tail scan.
type registration struct {
	// state is the published generation the baseline must be built from. It is
	// immutable (see commitState), which is what makes the build lock-free.
	state *leapmuxv1.UserCrdtState
	// maxHLC is that generation's max_hlc, cloned once. It IS
	// sub.resumeSuppressThrough -- the same value, not a second read -- and on
	// the RESUME arm it is also the scan's `until` high-water. One field rather
	// than two reads is what makes "the gate, the baseline and the tail bound
	// are one point in time" true by construction instead of by argument.
	maxHLC *leapmuxv1.HLC
	// filter is the private copy SubscriberController.Add captured -- the exact
	// filter every broadcast to this subscriber will be evaluated against.
	filter SubscriberFilter
	unsub  func()
}

// registerForFallback is the FALLBACK arm's counterpart to registerForResume:
// the O(1) window that registers the subscriber and captures the generation its
// baseline will be built from. The caller then builds that baseline holding
// NOTHING.
//
// Caller MUST hold subscribeExpandMu (the resolve→register TOCTOU) and MUST NOT
// hold m.projection; this takes and releases m.projection and m.mu.RLock.
//
// WHY THERE IS NEITHER A GAP NOR A DUPLICATE. commitState takes m.mu.Lock, so
// it is atomic with respect to the RLock below and every batch falls on exactly
// one side of it:
//
//   - commitState(B) BEFORE the RLock: B's ops are in reg.state, and
//     at >= B.hlc. If broadcastBatch(B) lands after the Add, sendTo's
//     resumeSuppressThrough gate drops it. Not delivered twice.
//   - commitState(B) AFTER the RUnlock: B is not in reg.state, and
//     broadcastBatch(B) follows commitState(B), which follows the Add and its
//     atomic snapshot publish -- so B is delivered live, and at < B.hlc leaves
//     the gate open. Not lost.
//   - There is no third case.
//
// resumeSuppressThrough is therefore set from the SAME generation the baseline
// is built from, and the invariant is one comparison:
// sub.resumeSuppressThrough == initial.MaxHlc.
//
// It must be assigned BEFORE registerSubscriber, because Add publishes a deep
// CLONE of the subscriber and sendTo reads the clone.
//
// m.projection is held here not for the baseline but as the REBASELINE FENCE.
// m.subscribers.Remove takes only the controller mutex while broadcastBatch
// loads the atomic snapshot after taking m.projection, so after a resume's
// unsub() an in-flight fan-out can still park a frame. Acquiring m.projection
// afterwards is a complete fence -- a fan-out either finished before we got it
// or starts after we release -- and holding it across {discard, register,
// capture} makes "discard and re-register are atomic with respect to broadcast"
// one statement rather than a fence plus an ordering argument. The whole window
// is O(1), so the cold path pays nothing for sharing it.
func (m *Manager) registerForFallback(sub *Subscriber, entry fallbackEntry) registration {
	m.projection.Lock()
	defer m.projection.Unlock()
	return m.registerForFallbackLocked(sub, entry)
}

// registerForFallbackLocked is registerForFallback's body, with m.projection
// left to the caller.
//
// It exists because the ladder's terminal rung must keep m.projection held
// ACROSS the baseline build (that is what makes overflow impossible there),
// while every other caller wants it released as soon as the window closes.
// Splitting the hold from the window keeps ONE copy of what the window
// captures: the whole no-gap/no-duplicate argument above is stated once and
// applies to both rungs. Hand-mirroring it in the escalated arm is what let the
// two copies drift apart — that arm had stopped honouring `entry` at all.
//
// Caller MUST hold m.projection and MUST NOT hold m.mu.
func (m *Manager) registerForFallbackLocked(sub *Subscriber, entry fallbackEntry) registration {
	if entry == fallbackRebaseline && sub.OnRebaseline != nil {
		sub.OnRebaseline()
	}
	return m.registerLocked(sub)
}

// registerLocked is THE register window, shared by both arms: capture the
// published generation, set the suppress gate from it, register the subscriber,
// and mint the one unsub handle for that registration.
//
// One implementation rather than two, because the no-gap/no-duplicate proof
// above is a property of this exact sequence and the RESUME arm depends on it
// just as much: its `until` high-water and the live path's suppress gate must be
// the same point in time or the tail and the broadcast stream can overlap or
// gap. Two hand-mirrored copies made that a coincidence of two independent reads
// of m.state; one copy makes it the same field.
//
// It also mints the unsub ONCE. makeUnsub returns a fresh sync.Once per call,
// so a second handle over the same subscriber would be free to decrement the
// presence refcount again -- the underflow makeUnsub's idempotence exists to
// prevent, reached by having two of them rather than by calling one twice.
//
// Caller MUST hold m.projection and MUST NOT hold m.mu.
func (m *Manager) registerLocked(sub *Subscriber) registration {
	var reg registration
	// BOTH unlocks are deferred, for the reason SubscribeWithACL's lock plan
	// gives: a recovered handler panic leaves the process serving, so a read
	// lock leaked while unwinding out of registerSubscriber (which reaches
	// subscribers.Add and presenceCtl.OnConnect) would block every later
	// commitState, decideResume and Materialized for this user forever.
	func() {
		m.mu.RLock()
		defer m.mu.RUnlock()
		reg.state = m.state
		reg.maxHLC = HLCClone(reg.state.GetMaxHlc())
		sub.resumeSuppressThrough = reg.maxHLC
		// Take the filter registerSubscriber RETURNS -- the private deep copy
		// SubscriberController.Add captured, which is the exact value every
		// broadcast to this subscriber is evaluated against. Reading sub.Filter
		// back afterwards would be value-equal today (both windows run under
		// subscribeExpandMu, the same lock contract/expand mutate it under), but
		// it sources the work's filter from the mutable field rather than from
		// the published copy.
		reg.filter = m.registerSubscriber(sub)
	}()
	reg.unsub = makeUnsub(sub, m)
	return reg
}

// registerSubscriber is the register half of both register windows
// (registerForFallback / registerForResume): Add + presence refcount. Split out
// so every arm registers through the SAME seam instead of hand-mirroring the
// Add+OnConnect pair, keeping the presence-refcount invariant in one place. The matching teardown
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
// (before re-entering the FALLBACK seam), since calling it twice is safe.
func makeUnsub(sub *Subscriber, m *Manager) func() {
	var unsubOnce sync.Once
	return func() {
		unsubOnce.Do(func() {
			m.subscribers.Remove(sub)
			m.presenceCtl.OnDisconnect(sub.ClientID)
		})
	}
}
