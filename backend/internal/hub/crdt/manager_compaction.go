package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TickHousekeeping runs one pass of the dedup-cleanup + epoch-advance
// + compaction cycle. Identical to what the 60s ticker inside Start
// triggers; exported so admin tooling and integration tests can force
// a deterministic pass without waiting on the ticker.
func (m *Manager) TickHousekeeping(ctx context.Context) {
	m.tickHousekeeping(ctx)
}

// tickHousekeeping runs dedup-table cleanup, lazy compaction, and
// epoch advance. Driven by a 60s ticker; never blocks long. Presence
// cleanup is event-driven (via subscriber disconnects + deferred
// clear timers), not bound to this tick.
func (m *Manager) tickHousekeeping(ctx context.Context) {
	if _, err := m.journal.CleanupExpiredRecentBatchIDs(ctx, m.now().Add(-DedupTTL)); err != nil {
		m.logger.Warn("cleanup recent batch ids", "err", err)
	}
	m.maybeAdvanceEpoch(ctx)
	m.maybeCompact(ctx)
}

func (m *Manager) maybeAdvanceEpoch(ctx context.Context) {
	if m.state.GetEpochStartedAt() == nil {
		return
	}
	started := m.state.GetEpochStartedAt().AsTime()
	if m.now().Sub(started) < EpochDuration {
		return
	}
	newEpoch := m.state.GetCurrentEpoch() + 1
	if err := m.journal.AdvanceEpoch(ctx, m.orgID, newEpoch, m.now()); err != nil {
		m.logger.Warn("advance epoch", "err", err)
		return
	}
	m.mu.Lock()
	m.state.CurrentEpoch = newEpoch
	m.state.EpochStartedAt = timestamppb.New(m.now())
	m.mu.Unlock()
}

func (m *Manager) maybeCompact(ctx context.Context) {
	// Cheap pre-check: if the watermark wouldn't advance there is
	// nothing to do, so skip the full CloneState on the idle path.
	m.mu.RLock()
	if HLCIsZero(m.state.GetMaxHlc()) || HLCCmp(m.state.GetMaxHlc(), m.state.GetCompactionWatermark()) <= 0 {
		m.mu.RUnlock()
		return
	}
	// Snapshot the in-memory state under the manager mutex so the
	// compaction walks a stable view.
	state := CloneState(m.state)
	m.mu.RUnlock()

	// Per-batch dedup rows are already in org_recent_batch_ids when
	// the batch committed; the compaction step only needs to ensure
	// the state blob carries the new watermark and the journal rows
	// whose last canonical HLC is ≤ watermark are dropped. The
	// retention contract on org_recent_batch_ids carries forward via
	// the existing expires_at column (set at commit time), so no
	// extra inserts are required here.
	state.CompactionWatermark = HLCClone(state.GetMaxHlc())

	// Drop tombstoned records whose tombstone_at is at or below the
	// new watermark. The state blob persisted by `CompactBatch` (one
	// transaction with the journal compaction) reflects the pruned
	// shape, so a fresh bootstrap sees the entity as never-existed.
	// See PruneTombstonesAtOrBelow's doc comment for the safety
	// argument.
	prunedCount := PruneTombstonesAtOrBelow(state, state.GetCompactionWatermark())

	if err := m.journal.CompactBatch(ctx, CompactBatch{
		State:       state,
		DropThrough: state.GetCompactionWatermark(),
	}); err != nil {
		m.logger.Warn("compaction", "err", err)
		return
	}
	m.mu.Lock()
	m.state.CompactionWatermark = HLCClone(state.GetCompactionWatermark())
	if prunedCount > 0 {
		// Apply the same prune to the in-memory state under the write
		// lock so reads after compaction see the pruned shape without
		// waiting for a journal reload.
		_ = PruneTombstonesAtOrBelow(m.state, m.state.GetCompactionWatermark())
	}
	m.mu.Unlock()
	if prunedCount > 0 {
		m.logger.Debug("compaction pruned tombstones", "count", prunedCount)
	}
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
