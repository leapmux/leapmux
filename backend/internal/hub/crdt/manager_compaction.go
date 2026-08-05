package crdt

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TickHousekeeping runs one pass of the dedup-cleanup + epoch-advance
// + compaction cycle. Identical to what the 60s ticker inside Start
// triggers; exported so admin tooling and integration tests can force
// a deterministic pass without waiting on the ticker. It returns once the
// pass has completed, so a caller can assert on its effects.
//
// The pass is ROUTED THROUGH the manager goroutine, the same way
// SeedStateForTest is and for the same reason: both maybeCompact and
// maybeAdvanceEpoch now PUBLISH a new state generation, and a publish from the
// caller's goroutine would discard any commit that landed between the clone and
// the swap. Running inline was tolerable only while the writeback stamped a
// couple of watermark fields onto whatever generation was live at the time.
// runOnManagerGoroutine carries the started/stopped/pre-Start negotiation,
// including why the pre-Start branch is safe only from the registry-factory
// shape.
//
// ctx bounds the JOURNAL WORK, not the wait: it is captured by the closure and
// consulted by the cleanup/compaction/epoch calls once the goroutine picks the
// task up. A caller that cancels while the task is still queued (or already
// running) still blocks until the pass returns. That is deliberate -- the pass
// publishes a state generation, so abandoning the wait would let the caller
// proceed against state that is about to change under it -- but it means a
// deadline'd caller can wait past its deadline if the goroutine is inside a
// slow submit.
//
// After Stop the goroutine has exited and the send falls through to <-m.done,
// so a post-Stop call returns without running the pass rather than wedging.
//
// MUST NOT be called FROM the manager goroutine -- it is a taskCh send, so the
// goroutine would be waiting on itself. See runOnManagerGoroutine. The 60s
// ticker inside Start calls the unexported tickHousekeeping instead, for
// exactly this reason.
func (m *Manager) TickHousekeeping(ctx context.Context) {
	m.runOnManagerGoroutine(func() { m.tickHousekeeping(ctx) })
}

// tickHousekeeping runs dedup-table cleanup, lazy compaction, and
// epoch advance. Driven by a 60s ticker; never blocks long. Presence
// cleanup is event-driven (via subscriber disconnects + deferred
// clear timers), not bound to this tick.
func (m *Manager) tickHousekeeping(ctx context.Context) {
	if _, err := m.journal.CleanupExpiredRecentBatchIDs(ctx, m.now()); err != nil {
		m.logger.Warn("cleanup recent batch ids", "err", err)
	}
	m.maybeAdvanceEpoch(ctx)
	m.maybeCompact(ctx)
}

func (m *Manager) maybeAdvanceEpoch(ctx context.Context) {
	// maybeAdvanceEpoch runs on the manager goroutine (via tickHousekeeping),
	// which is the sole writer of m.state, so the bare reads below need no
	// lock beyond m.mu (taken for the final write to exclude the RLock
	// readers).
	if m.state.GetEpochStartedAt() == nil {
		return
	}
	started := m.state.GetEpochStartedAt().AsTime()
	if m.now().Sub(started) < EpochDuration {
		return
	}
	newEpoch := m.state.GetCurrentEpoch() + 1
	if err := m.journal.AdvanceEpoch(ctx, m.owner.String(), newEpoch, m.now()); err != nil {
		m.logger.Warn("advance epoch", "err", err)
		return
	}
	// PUBLISH a new generation rather than editing the live one: a published
	// generation is immutable (see commitState), which is what lets a cold
	// subscriber capture the pointer and build its baseline lock-free. The
	// entity maps are aliased, so this stays O(1).
	next := nextStateGeneration(m.state)
	next.CurrentEpoch = newEpoch
	next.EpochStartedAt = timestamppb.New(m.now())
	m.commitState(next)
}

func (m *Manager) maybeCompact(ctx context.Context) {
	// Cheap pre-check: if the compaction watermark wouldn't advance there is
	// nothing to do, so skip the full CloneState on the idle path.
	//
	// This also skips the DropThrough delete, which is only safe because
	// laggedRetentionWatermark is a pure function of (max_hlc,
	// compaction_watermark, ttl): a max_hlc that has not moved pins the lagging
	// floor as surely as the tight one, so there is no retention work waiting
	// either. Note the reason is the SHARED INPUT, not the ordering -- being
	// bounded above by a stationary value would not on its own make a value
	// stationary. Anyone making retention wall-clock-driven invalidates this
	// pre-check and must move the DropThrough out from behind it, or a dormant
	// account's op rows would never be swept.
	m.mu.RLock()
	if HLCIsZero(m.state.GetMaxHlc()) || HLCCmp(m.state.GetMaxHlc(), m.state.GetCompactionWatermark()) <= 0 {
		m.mu.RUnlock()
		return
	}
	// Build the generation this pass will publish. The prune below only ADDS
	// OR REMOVES map entries -- it never writes a record field -- and the two
	// watermark stamps are header fields, so copying the maps and sharing the
	// records is sufficient. A full CloneState here deep-cloned every node, tab,
	// window and workspace through protobuf reflection on the manager goroutine,
	// once per tick on any active account, for a pass that then deletes a
	// handful of entries.
	state := nextStateGenerationWithOwnMaps(m.state)
	m.mu.RUnlock()

	// Per-batch dedup rows are already in user_recent_batch_ids when
	// the batch committed; the compaction step only needs to ensure
	// the state blob carries the new watermark and the journal rows
	// whose last canonical HLC is ≤ the retention watermark are dropped.
	// The retention contract on user_recent_batch_ids carries forward via
	// the existing expires_at column (set at commit time), so no extra
	// inserts are required here.

	// compaction_watermark advances to max_hlc: tombstone pruning is
	// load-bearing for correctness (HLC monotonicity, bounded user_state
	// growth), so it tracks the live head tightly.
	state.CompactionWatermark = HLCClone(state.GetMaxHlc())

	// op_retention_watermark LAGS compaction_watermark by OpRetentionTTL.
	// It is the floor for op-batch DELETION: DeleteUserOpBatchesThrough
	// (CompactBatch.DropThrough) drops rows whose last canonical HLC is ≤
	// it, so batches in (op_retention_watermark, compaction_watermark]
	// survive below the tombstone-prune line. That widened window is what
	// lets delta-resume pay off across multi-hour gaps instead of only the
	// ~60s between compaction ticks — a client whose cursor is above this
	// watermark (and below compaction_watermark) still resumes (decideResume
	// gates on op_retention_watermark, not compaction_watermark). The op
	// log is an append-only history and replaying a longer tail is sound
	// (each op is idempotent under the CRDT merge), so retention is pure
	// storage optimization, decoupled from the correctness-tight prune.
	//
	// The lag is applied to max_hlc.physical (a UnixMilli value), keeping
	// the same client_id and zeroing logical: the watermark is a deletion
	// floor, not a real HLC, and a logical > 0 would narrow the deleted
	// range to a sub-millisecond sliver that has no meaning as a floor.
	// Clamp at ≤ compaction_watermark so retention can never precede the
	// prune line (and the invariant op_retention_watermark ≤
	// compaction_watermark ≤ max_hlc always holds). On the first compaction
	// of a young state where max_hlc.physical < OpRetentionTTL, the floor
	// goes negative; clamp it to zero (HLCIsZero) so decideResume's
	// `cursor > op_retention_watermark` still admits any non-zero cursor —
	// i.e. every resumable client until the state is older than the TTL.
	opRetention := laggedRetentionWatermark(state.GetMaxHlc(), state.GetCompactionWatermark(), m.opRetentionTTL)
	state.OpRetentionWatermark = opRetention

	// Drop tombstoned records whose tombstone_at is at or below the
	// new compaction_watermark (the TIGHT one — pruning is correctness-
	// bound, not retention-bound). The state blob persisted by
	// `CompactBatch` (one transaction with the journal compaction) reflects
	// the pruned shape, so a fresh bootstrap sees the entity as
	// never-existed. See PruneTombstonesAtOrBelow's doc comment for the
	// safety argument.
	prunedCount := PruneTombstonesAtOrBelow(state, state.GetCompactionWatermark())

	if err := m.journal.CompactBatch(ctx, CompactBatch{
		State:       state,
		DropThrough: state.GetOpRetentionWatermark(),
	}); err != nil {
		m.logger.Warn("compaction", "err", err)
		return
	}
	// PUBLISH the clone we just persisted, rather than replaying the watermark
	// writes and the prune onto the live generation. `state` already carries
	// both watermarks and the pruned shape, and this runs on the manager
	// goroutine -- the sole writer -- so nothing can have touched m.state
	// between the CloneState above and here. Publishing is both less code and
	// the only version that upholds commitState's immutability invariant: the
	// prune DELETES map entries, and CloneStateForBatch shares untouched maps
	// across generations, so an in-place prune mutated older generations too --
	// which a lock-free reader (materializedFromState) would race.
	m.commitState(state)
	if prunedCount > 0 {
		m.logger.Debug("compaction pruned tombstones", "count", prunedCount)
	}
}

// laggedRetentionWatermark derives the op_retention_watermark from max_hlc by
// subtracting ttl (in physical-ms). It is clamped to be at most
// compactionWatermark (so retention never precedes the prune line) and at
// least zero (so a young state whose max_hlc.physical < ttl yields a zero
// floor that admits every non-zero cursor through decideResume).
//
// The watermark is a deletion FLOOR: DeleteUserOpBatchesThrough drops rows
// whose last canonical HLC is ≤ it. Two cases:
//
//  1. The lag moved physical-ms strictly earlier (ttl > 0 and
//     max_hlc.physical ≥ ttl): the floor is an earlier millisecond, so the
//     sub-millisecond logical of max_hlc is irrelevant — zero it and carry
//     max_hlc's client_id. Every batch at max_hlc.physical (any logical) is
//     strictly > the floor and survives, which is correct: those batches are
//     the most recent and must outlive a positive retention window.
//  2. The lag did NOT move physical-ms (ttl == 0, or the clamp pinned the
//     floor to compaction_watermark): the floor IS max_hlc, so it must carry
//     max_hlc's logical too — otherwise a batch sharing max_hlc.physical with
//     logical > 0 would survive the `> floor` keep-test and the zero-TTL
//     contract ("drop everything at or below the head") would leak. Return
//     compactionWatermark (== max_hlc) verbatim in this case.
func laggedRetentionWatermark(maxHlc, compactionWatermark *leapmuxv1.HLC, ttl time.Duration) *leapmuxv1.HLC {
	phys := maxHlc.GetPhysical() - int64(ttl/time.Millisecond)
	if phys < 0 {
		// Zero (HLCIsZero) — decideResume's `cursor > op_retention_watermark`
		// treats nil/zero as less-than any non-zero cursor, so every
		// resumable client passes until the state outlives the TTL.
		return &leapmuxv1.HLC{}
	}
	if phys >= maxHlc.GetPhysical() {
		// ttl == 0 (or negative, defensively): the floor is the head itself.
		// Return compactionWatermark verbatim so its logical is preserved and
		// the `> floor` keep-test drops every batch at or below the head.
		return HLCClone(compactionWatermark)
	}
	wm := &leapmuxv1.HLC{
		Physical: phys,
		ClientId: maxHlc.GetClientId(),
	}
	// Clamp at ≤ compactionWatermark. Normally wm.physical < max_hlc.physical
	// == compactionWatermark.physical, so wm < compactionWatermark already;
	// the clamp only bites if compactionWatermark was set by an earlier tick
	// and lags max_hlc (defensive — the invariant wm ≤ cw must always hold).
	if HLCCmp(wm, compactionWatermark) > 0 {
		return HLCClone(compactionWatermark)
	}
	return wm
}

// makeDedupHitResult reconstructs the original BatchCommitted from a
// stored dedup row. Per-op canonical HLCs derive from
// (first.Physical, first.Logical+i, first.ClientId) since the manager
// minted contiguous-logical HLCs at commit time within a single Tick
// window.
func makeDedupHitResult(batch *leapmuxv1.OpBatch, row *RecentBatchRecord, epoch int64) *leapmuxv1.BatchResult {
	ops := batch.GetOps()
	first := row.CanonicalFirstHLC
	committed := make([]*leapmuxv1.CommittedOp, len(ops))
	for i, op := range ops {
		committed[i] = &leapmuxv1.CommittedOp{
			OpId: op.GetOpId(),
			CanonicalHlc: &leapmuxv1.HLC{
				Physical: first.GetPhysical(),
				Logical:  first.GetLogical() + int64(i),
				ClientId: first.GetClientId(),
			},
		}
	}
	max := &leapmuxv1.HLC{
		Physical: first.GetPhysical(),
		Logical:  first.GetLogical() + row.OpCount - 1,
		ClientId: first.GetClientId(),
	}
	return &leapmuxv1.BatchResult{
		BatchId: batch.GetBatchId(),
		Outcome: &leapmuxv1.BatchResult_Committed{
			Committed: &leapmuxv1.BatchCommitted{
				Committed: committed,
				MaxHlc:    max,
				Epoch:     epoch,
			},
		},
	}
}
