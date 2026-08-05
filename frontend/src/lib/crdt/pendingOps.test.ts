import type { HydrationPayload } from './hydrate'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { HLCSchema, NodeKind, NodeRecordSchema, UserCrdtStateSchema } from '~/generated/leapmux/v1/user_crdt_pb'
import {
  BatchCommittedSchema,
  BatchRejectionReason,
  BatchRejectionSchema,
  CommittedOpSchema,
  EntityMaterializedSchema,
  EntityRemovedSchema,
  OpBatchSchema,
  ResumeDeltaSchema,
  TabIdentSchema,
  WatchUserEventSchema,
} from '~/generated/leapmux/v1/user_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { HLCClock } from './hlc'
import { newBatch, setNodeKind, setTabTileId, tombstoneTab } from './ops'
import { MAX_OWN_ECHO_BATCH_IDS, PendingOpsManager, pruneTombstonesAtOrBelow } from './pendingOps'

function makeMgr(notify?: () => void) {
  const clock = new HLCClock('clientA')
  return new PendingOpsManager('user', clock, notify)
}

describe('pendingOpsManager', () => {
  let mgr: PendingOpsManager

  beforeEach(() => {
    mgr = makeMgr()
  })

  it('submit applies the batch speculatively (canonical_hlc fallback)', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    mgr.submit(batch)
    // speculativeState should reflect n1 living as a leaf.
    const node = mgr.state.speculativeState.nodes.n1
    expect(node).toBeDefined()
    expect(node?.kind?.value).toBe(NodeKind.LEAF)
    // confirmedState should NOT have it yet.
    expect(mgr.state.confirmedState.nodes.n1).toBeUndefined()
  })

  it('consumeRemote on an echoed batch drops it from pending and applies to confirmed', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    mgr.submit(batch)
    expect(mgr.state.pendingBatches.length).toBe(1)
    // Hub echoes back the same batch_id with a canonical_hlc stamped.
    batch.ops[0].canonicalHlc = create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' })
    mgr.consumeRemote(batch)
    // Pending batch has been removed (matched by batchId).
    expect(mgr.state.pendingBatches.length).toBe(0)
    expect(mgr.state.confirmedState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
    expect(mgr.state.speculativeState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
  })

  it('consumeRemote on a non-echoed batch applies to confirmed and recomputes speculative', () => {
    const op = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'remote-node', NodeKind.SPLIT)
    op.canonicalHlc = create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' })
    mgr.consumeRemote(newBatch([op]))
    expect(mgr.state.confirmedState.nodes['remote-node']?.kind?.value).toBe(NodeKind.SPLIT)
    expect(mgr.state.speculativeState.nodes['remote-node']?.kind?.value).toBe(NodeKind.SPLIT)
  })

  it('applyDelta folds the tail into confirmed WITHOUT a wholesale reset, advances the watermark, and keeps pendingBatches', () => {
    // Seed confirmed state + a watermark via bootstrap so applyDelta has a
    // baseline to fold onto (not replace).
    mgr.bootstrap({
      userId: 'user',
      nodes: { exist: create(NodeRecordSchema, { nodeId: 'exist' }) },
      tabs: {},
      floatingWindows: {},
      workspaces: {},
      maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 5n,
    })
    // A locally-pending optimistic batch must survive a delta (same as it
    // survives a full-snapshot re-bootstrap).
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const pending = newBatch([setNodeKind(ctx, 'pending-node', NodeKind.LEAF)])
    mgr.submit(pending)
    expect(mgr.state.pendingBatches).toHaveLength(1)

    const deltaOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'delta-node', NodeKind.GRID)
    deltaOp.canonicalHlc = create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' })
    mgr.applyDelta(create(ResumeDeltaSchema, {
      frames: [create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([deltaOp]) } })],
      maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 5n,
    }))

    // The delta op folded onto confirmed; the pre-existing bootstrap node is
    // STILL there (no wholesale replace).
    expect(mgr.state.confirmedState.nodes['delta-node']?.kind?.value).toBe(NodeKind.GRID)
    expect(mgr.state.confirmedState.nodes.exist).toBeDefined()
    // Pending batch survived.
    expect(mgr.state.pendingBatches).toHaveLength(1)
    // Watermark advanced to the delta's max op (300), and the epoch the hub
    // reported is reflected in currentEpoch (which doubles as the resume epoch).
    expect(mgr.state.resumeWatermark?.physical).toBe(300n)
    expect(mgr.state.currentEpoch).toBe(5n)
  })

  it('applyDelta observes delta.maxHlc into the clock so the local clock does not lag the resume high-water mark', () => {
    // The delta's max_hlc is the filtered tail's max op, which the hub reports
    // from the subscriber's allowed set. If the op that advanced the state's
    // true max sat in a filtered-out workspace, the tail's maxHlc still
    // reflects the allowed tail, and the client's HLC clock must observe it so
    // a subsequent local submit() does not mint a client_hlc below the resume
    // point. Mirrors bootstrap's clock.observe(snapshot.maxHlc).
    //
    // The resume watermark must ALSO adopt max_hlc (ResumeDelta wire contract),
    // not only per-frame HLCs — otherwise a max_hlc ahead of applied frame HLCs
    // leaves the next reconnect re-pulling an already-covered gap.
    mgr.bootstrap({
      userId: 'user',
      nodes: {},
      tabs: {},
      floatingWindows: {},
      workspaces: {},
      maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    })
    // A delta tail op at physical=200; delta.maxHlc at physical=500 (simulating
    // the state's true max sitting above the tail, e.g. an op the filter kept).
    const deltaOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'delta-node', NodeKind.LEAF)
    deltaOp.canonicalHlc = create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' })
    mgr.applyDelta(create(ResumeDeltaSchema, {
      frames: [create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([deltaOp]) } })],
      maxHlc: create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    }))
    // The clock observed the delta's max (500), not just the tail op (200), so
    // current().physical is at least 500 — a later tick() can only go higher.
    expect(mgr.clock.current().physical).toBe(500n)
    expect(mgr.state.resumeWatermark?.physical).toBe(500n)
  })

  it('applyDelta dedups a batch already applied (echo of a pending batch) by batch_id', () => {
    mgr.bootstrap({
      userId: 'user',
      nodes: {},
      tabs: {},
      floatingWindows: {},
      workspaces: {},
      maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    })
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const pending = newBatch([setNodeKind(ctx, 'echoed', NodeKind.LEAF)])
    mgr.submit(pending)
    // The same batch_id arrives in a delta (an echo): it is dropped from
    // pending and NOT double-applied (the op is still applied once, matching
    // consumeRemote's echo semantics).
    const echoOp = pending.ops[0]
    echoOp.canonicalHlc = create(HLCSchema, { physical: 150n, logical: 0n, clientId: 'hub' })
    mgr.applyDelta(create(ResumeDeltaSchema, {
      frames: [create(WatchUserEventSchema, {
        event: { case: 'batch', value: create(OpBatchSchema, { batchId: pending.batchId, ops: [echoOp] }) },
      })],
      maxHlc: create(HLCSchema, { physical: 150n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    }))
    expect(mgr.state.pendingBatches).toHaveLength(0)
    expect(mgr.state.confirmedState.nodes.echoed?.kind?.value).toBe(NodeKind.LEAF)
  })

  it('applyDelta evicts an entity via a removed frame and reports droppedPending', () => {
    // #357: the resume delta now carries materialized/removed frames. A removed
    // frame evicts the entity from confirmedState AND drops any pending op on it
    // (otherwise a pending mutation would resurrect a redacted entity). applyDelta
    // aggregates droppedPending across frames so a single warn-toast covers the
    // whole delta — mirroring consumeEntityRemoved's live-broadcast behavior.
    mgr.bootstrap({
      userId: 'user',
      nodes: {},
      tabs: { tA: { tabId: 'tA', tabType: TabType.AGENT } },
      floatingWindows: {},
      workspaces: {},
      maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    })
    // A pending op on tA that the removed frame must drop.
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    mgr.submit(newBatch([setTabTileId(ctx, TabType.AGENT, 'tA', 'tile')]))
    expect(mgr.state.pendingBatches).toHaveLength(1)

    const result = mgr.applyDelta(create(ResumeDeltaSchema, {
      frames: [create(WatchUserEventSchema, {
        event: {
          case: 'entityRemoved',
          value: create(EntityRemovedSchema, {
            atHlc: create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' }),
            entity: { case: 'tab', value: create(TabIdentSchema, { tabType: TabType.AGENT, tabId: 'tA' }) },
          }),
        },
      })],
      maxHlc: create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    }))
    // The tab was evicted from confirmedState...
    expect(mgr.state.confirmedState.tabs.tA).toBeUndefined()
    // ...and the pending op touching it was dropped, with the flag surfaced so
    // the caller (useUserEvents) can fire onPendingDropped.
    expect(mgr.state.pendingBatches).toHaveLength(0)
    expect(result.droppedPending).toBe(true)
  })

  it('applyDelta installs an entity via a materialized frame', () => {
    // #357 symmetric case: an entity that crossed INTO the subscriber's allowed
    // set during the gap arrives as a materialized frame carrying the full
    // record (not a raw move op that would leak pre-state from a hidden
    // workspace). applyDelta installs it into confirmedState.
    mgr.bootstrap({
      userId: 'user',
      nodes: {},
      tabs: {},
      floatingWindows: {},
      workspaces: {},
      maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    })
    mgr.applyDelta(create(ResumeDeltaSchema, {
      frames: [create(WatchUserEventSchema, {
        event: {
          case: 'entityMaterialized',
          value: create(EntityMaterializedSchema, {
            atHlc: create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' }),
            entity: { case: 'tab', value: { tabId: 'tNew', tabType: TabType.AGENT } },
          }),
        },
      })],
      maxHlc: create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    }))
    expect(mgr.state.confirmedState.tabs.tNew?.tabId).toBe('tNew')
  })

  it('isConfirmedPopulated is false before bootstrap and true once any entity map holds a record', () => {
    // The resume refresh-guard uses this to decide whether a delta is safe to
    // fold onto existing state. It must count ANY top-level entity map as
    // populated (not just workspaces), so a state whose only record is, e.g.,
    // a floating window still counts.
    expect(mgr.isConfirmedPopulated()).toBe(false)
    mgr.bootstrap({
      userId: 'user',
      nodes: {},
      tabs: {},
      floatingWindows: { fw1: { windowId: 'fw1' } },
      workspaces: {},
      maxHlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'hub' }),
      currentEpoch: 1n,
    })
    expect(mgr.isConfirmedPopulated()).toBe(true)
  })

  it('consumeBatchCommitted replaces clientHlc with canonicalHlc and applies to confirmed', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.GRID)])
    mgr.submit(batch)
    const opId = batch.ops[0].opId
    const canonical = create(HLCSchema, { physical: 500n, logical: 3n, clientId: 'hub' })
    mgr.consumeBatchCommitted(batch.batchId, create(BatchCommittedSchema, {
      committed: [create(CommittedOpSchema, { opId, canonicalHlc: canonical })],
      maxHlc: canonical,
      epoch: 7n,
    }))
    expect(mgr.state.confirmedState.nodes.n1?.kind?.value).toBe(NodeKind.GRID)
    // currentEpoch absorbed.
    expect(mgr.state.currentEpoch).toBe(7n)
    // Pending batch consumed.
    expect(mgr.state.pendingBatches.length).toBe(0)
  })

  it('consumeBatchCommitted refreshes currentEpoch even when no pending batch matches (idx < 0)', () => {
    // Covers the EPOCH_REQUIRED-recovery branch: a batch reverted client-side
    // (or one that originated on another client) has no entry in pendingBatches,
    // but the hub's committed echo still carries the authoritative epoch. Skipping
    // the refresh would leave currentEpoch stale so the next SubmitOps gets
    // STALE_EPOCH and re-triggers a reconnect cycle. The branch also notifies
    // (so the persistence effect re-evaluates) without touching confirmedState.
    expect(mgr.state.currentEpoch).toBe(1n)
    mgr.consumeBatchCommitted('batch-this-client-never-submitted', create(BatchCommittedSchema, {
      committed: [],
      maxHlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'hub' }),
      epoch: 42n,
    }))
    // Epoch is authoritative regardless of whether the batch was pending here.
    expect(mgr.state.currentEpoch).toBe(42n)
    // No pending batch to consume, confirmedState untouched.
    expect(mgr.state.pendingBatches.length).toBe(0)
    // The resume watermark must NOT advance from a committed echo for a batch
    // this client never held (no canonical op was applied here).
    expect(mgr.state.resumeWatermark).toBeUndefined()
  })

  it('consumeBatchRejected drops the batch and reports retryable=false for non-retryable reasons', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    mgr.submit(batch)
    const result = mgr.consumeBatchRejected(batch.batchId, create(BatchRejectionSchema, {
      reason: 10, // BATCH_REJECTION_TAB_PLACEMENT_INVALID
      offendingOpId: batch.ops[0].opId,
    }))
    expect(result.retryable).toBe(false)
    expect(result.reason).toBe(10)
    expect(mgr.state.pendingBatches.length).toBe(0)
    // Speculative state recomputed → n1 dropped.
    expect(mgr.state.speculativeState.nodes.n1).toBeUndefined()
  })

  it('consumeBatchRejected reports retryable=true only for EPOCH_REQUIRED', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    mgr.submit(batch)
    const result = mgr.consumeBatchRejected(batch.batchId, create(BatchRejectionSchema, {
      reason: BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
      offendingOpId: batch.ops[0].opId,
    }))
    expect(result.retryable).toBe(true)
  })

  it('consumeBatchRejected fails safe: unknown and permanent reasons are NOT retryable', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    // The retryable set is an allowlist, so a reason absent from it -- an
    // unspecified/transport code (0), a reason added to the proto later
    // (INVALID_WORKER_REF, a permanent CanUseWorker deny), or an out-of-range
    // value -- defaults to non-retryable rather than looping forever.
    for (const reason of [
      0, // UNSPECIFIED — transport-failure / unknown
      BatchRejectionReason.BATCH_REJECTION_INVALID_WORKER_REF,
      BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH,
      99, // out of the enum range entirely
    ]) {
      const batch = newBatch([setNodeKind(ctx, `n-${reason}`, NodeKind.LEAF)])
      mgr.submit(batch)
      const result = mgr.consumeBatchRejected(batch.batchId, create(BatchRejectionSchema, {
        reason: reason as BatchRejectionReason,
        offendingOpId: '',
      }))
      expect(result.retryable).toBe(false)
    }
  })

  it('keeps a retryable batch applied on rejection; revertPendingBatch drops it', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    mgr.submit(batch)
    expect(mgr.state.speculativeState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)

    // A retryable (EPOCH_REQUIRED) rejection is NOT terminal -- the submitter
    // requeues the batch -- so its optimistic ops stay applied (no
    // revert-then-reapply flicker across the reconnect+retry window).
    const result = mgr.consumeBatchRejected(batch.batchId, create(BatchRejectionSchema, {
      reason: BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
      offendingOpId: batch.ops[0].opId,
    }))
    expect(result.retryable).toBe(true)
    expect(mgr.state.pendingBatches.length).toBe(1)
    expect(mgr.state.speculativeState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)

    // When the submitter finally gives up, revertPendingBatch drops + reverts it.
    mgr.revertPendingBatch(batch.batchId)
    expect(mgr.state.pendingBatches.length).toBe(0)
    expect(mgr.state.speculativeState.nodes.n1).toBeUndefined()
  })

  it('a kept retryable batch is reconciled (not double-applied) when the retry commits', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.GRID)])
    mgr.submit(batch)
    mgr.consumeBatchRejected(batch.batchId, create(BatchRejectionSchema, {
      reason: BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
      offendingOpId: batch.ops[0].opId,
    }))
    expect(mgr.state.pendingBatches.length).toBe(1) // kept applied

    // The requeued retry commits: the still-pending batch is reconciled into
    // confirmed and dropped from pending exactly once.
    const canonical = create(HLCSchema, { physical: 900n, logical: 1n, clientId: 'hub' })
    mgr.consumeBatchCommitted(batch.batchId, create(BatchCommittedSchema, {
      committed: [create(CommittedOpSchema, { opId: batch.ops[0].opId, canonicalHlc: canonical })],
      maxHlc: canonical,
      epoch: 8n,
    }))
    expect(mgr.state.pendingBatches.length).toBe(0)
    expect(mgr.state.confirmedState.nodes.n1?.kind?.value).toBe(NodeKind.GRID)
  })

  it('consumeEntityMaterialized installs a fresh tab record into confirmedState', () => {
    const tabRecord = {
      $typeName: 'leapmux.v1.TabRecord' as const,
      tabType: TabType.AGENT,
      tabId: 'tNew',
      tileId: undefined,
      position: undefined,
      workerId: undefined,
      displayMode: undefined,
      fileViewMode: undefined,
      fileDiffBase: undefined,
      tombstoneAt: undefined,
    }
    const evt = create(EntityMaterializedSchema, {
      atHlc: create(HLCSchema, { physical: 1000n, logical: 0n, clientId: 'hub' }),
      entity: { case: 'tab', value: tabRecord as never },
    })
    mgr.consumeEntityMaterialized(evt)
    expect(mgr.state.confirmedState.tabs.tNew).toBeDefined()
  })

  it('consumeEntityRemoved drops pending ops touching that tab', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([
      setTabTileId(ctx, TabType.AGENT, 'tDoomed', 'someTile'),
      setNodeKind(ctx, 'unrelated-node', NodeKind.LEAF),
    ])
    mgr.submit(batch)
    expect(mgr.state.pendingBatches[0].ops.length).toBe(2)

    const evt = create(EntityRemovedSchema, {
      atHlc: create(HLCSchema, { physical: 2000n, logical: 0n, clientId: 'hub' }),
      entity: {
        case: 'tab',
        value: create(TabIdentSchema, { tabType: TabType.AGENT, tabId: 'tDoomed' }),
      },
    })
    const result = mgr.consumeEntityRemoved(evt)
    expect(result.droppedPending).toBe(true)
    // The unrelated op should still be in the pending batch.
    expect(mgr.state.pendingBatches[0].ops.length).toBe(1)
    const remaining = mgr.state.pendingBatches[0].ops[0]
    expect(remaining.body.case).toBe('setNodeRegister')
  })

  it('notify is invoked after every state-mutating method', () => {
    const notify = vi.fn()
    const m = makeMgr(notify)
    const ctx = { originClientId: 'clientA', clock: m.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    m.submit(batch)
    expect(notify).toHaveBeenCalledTimes(1)

    batch.ops[0].canonicalHlc = create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' })
    m.consumeRemote(batch)
    expect(notify).toHaveBeenCalledTimes(2)
  })

  it('canonical HLC swap recomputes speculative when LWW outcome changes', () => {
    // Two pending batches both write tile_id on the same tab. The
    // second batch's submission-order client_hlc is larger than the
    // first's, so optimistically tA.tile_id == 'B'. When the FIRST
    // batch commits with a canonical_hlc that's larger than the
    // second's client_hlc, recompute must yield 'A'.
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    const t = TabType.AGENT
    // Initial tab placement (so tile_id register exists).
    mgr.submit(newBatch([
      setTabTileId(ctx, t, 'tA', 'rootTile'),
    ]))

    const b1 = newBatch([setTabTileId(ctx, t, 'tA', 'A')])
    const b2 = newBatch([setTabTileId(ctx, t, 'tA', 'B')])
    mgr.submit(b1)
    mgr.submit(b2)
    // Second submission's clientHlc is larger → speculative says 'B'.
    expect(mgr.state.speculativeState.tabs.tA?.tileId?.value).toBe('B')

    // Hub commits b1 with a higher canonical HLC than b2's clientHlc.
    const opB2 = b2.ops[0]
    const b1Canonical = create(HLCSchema, {
      physical: opB2.clientHlc!.physical + 100n,
      logical: 0n,
      clientId: 'hub',
    })
    mgr.consumeBatchCommitted(b1.batchId, create(BatchCommittedSchema, {
      committed: [create(CommittedOpSchema, { opId: b1.ops[0].opId, canonicalHlc: b1Canonical })],
      maxHlc: b1Canonical,
      epoch: 1n,
    }))
    // After commit, b1 is gone from pending; b2 is still pending. The
    // speculative recompute must see canonical_hlc(b1) > clientHlc(b2)
    // → tA.tileId should now be 'A' (b1 wins LWW).
    expect(mgr.state.speculativeState.tabs.tA?.tileId?.value).toBe('A')
  })

  it('tombstoneTab consumed remotely propagates to confirmed and speculative states', () => {
    const ctx = { originClientId: 'clientA', clock: mgr.clock }
    // Seed tA via a remote batch so we have a confirmed record.
    const seedOp = setTabTileId(ctx, TabType.AGENT, 'tA', 'rootTile')
    seedOp.canonicalHlc = create(HLCSchema, { physical: 50n, logical: 0n, clientId: 'remote' })
    mgr.consumeRemote(newBatch([seedOp]))
    expect(mgr.state.confirmedState.tabs.tA).toBeDefined()
    expect(mgr.state.confirmedState.tabs.tA?.tileId?.value).toBe('rootTile')

    const tombstone = tombstoneTab(ctx, TabType.AGENT, 'tA')
    tombstone.canonicalHlc = create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'remote' })
    mgr.consumeRemote(newBatch([tombstone]))
    expect(mgr.state.confirmedState.tabs.tA?.tombstoneAt).toBeDefined()
    expect(mgr.state.confirmedState.tabs.tA?.tombstoneAt?.physical).toBe(100n)
    // Tombstoned: tile_id register cleared.
    expect(mgr.state.confirmedState.tabs.tA?.tileId).toBeUndefined()
  })

  describe('speculativeState alias optimization', () => {
    // Pin the fast-path contract: when no batches are pending,
    // speculativeState IS confirmedState (same object reference) so
    // recomputeSpeculative can skip the cloneState+replay cost.
    // submit() must detach the alias before its first mutation; if it
    // doesn't, a local op would silently pollute confirmedState and
    // the next remote echo would observe a "double-apply".

    it('recomputeSpeculative aliases when pending is empty', () => {
      // Constructor seeds distinct fresh maps; the alias is
      // established once recomputeSpeculative runs (which happens on
      // every mutating path: bootstrap, consumeRemote, etc.).
      expect(mgr.state.pendingBatches.length).toBe(0)
      mgr.recomputeSpeculative()
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('alias holds after a remote batch lands (still empty pending)', () => {
      const op = setNodeKind({ originClientId: 'remote', clock: new HLCClock('remote') }, 'r1', NodeKind.SPLIT)
      op.canonicalHlc = create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'remote' })
      mgr.consumeRemote(newBatch([op]))
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
      // And both see the remote change (since they're literally the same proto).
      expect(mgr.state.speculativeState.nodes.r1?.kind?.value).toBe(NodeKind.SPLIT)
    })

    it('submit detaches the alias before applying the local op', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      // Establish the alias first via recomputeSpeculative so the
      // detach contract is observable.
      mgr.recomputeSpeculative()
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
      const batch = newBatch([setNodeKind(ctx, 'local1', NodeKind.LEAF)])
      mgr.submit(batch)
      // Post-submit: distinct objects.
      expect(mgr.state.speculativeState).not.toBe(mgr.state.confirmedState)
      // Speculative sees the local op, confirmed does NOT — without
      // the detach, applyOp would have mutated confirmedState too.
      expect(mgr.state.speculativeState.nodes.local1?.kind?.value).toBe(NodeKind.LEAF)
      expect(mgr.state.confirmedState.nodes.local1).toBeUndefined()
    })

    it('alias re-establishes after the last pending batch settles via echo', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
      mgr.submit(batch)
      expect(mgr.state.speculativeState).not.toBe(mgr.state.confirmedState)

      // Hub echoes; pending list drains.
      batch.ops[0].canonicalHlc = create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(batch)
      expect(mgr.state.pendingBatches.length).toBe(0)
      // Back to aliased after pending drains.
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('alias re-establishes after batch-committed drains the pending queue', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n2', NodeKind.LEAF)])
      mgr.submit(batch)
      const committed = create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, {
          opId: batch.ops[0].opId,
          canonicalHlc: create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' }),
        })],
      })
      mgr.consumeBatchCommitted(batch.batchId, committed)
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('alias re-establishes after batch-rejected drains the pending queue', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n3', NodeKind.LEAF)])
      mgr.submit(batch)
      // The rejection reason is immaterial here -- this test only cares
      // about alias re-establishment after the pending queue drains, not
      // the retryable classification.
      const rejection = create(BatchRejectionSchema, {
        reason: 99 as any,
        offendingOpId: batch.ops[0].opId,
      })
      mgr.consumeBatchRejected(batch.batchId, rejection)
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('alias stays detached while multiple batches are pending, then re-aliases when all drain', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const b1 = newBatch([setNodeKind(ctx, 'a', NodeKind.LEAF)])
      const b2 = newBatch([setNodeKind(ctx, 'b', NodeKind.LEAF)])
      mgr.submit(b1)
      mgr.submit(b2)
      expect(mgr.state.speculativeState).not.toBe(mgr.state.confirmedState)
      // After the first echo, one batch still pending → still detached.
      b1.ops[0].canonicalHlc = create(HLCSchema, { physical: 10n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(b1)
      expect(mgr.state.pendingBatches.length).toBe(1)
      expect(mgr.state.speculativeState).not.toBe(mgr.state.confirmedState)
      // Second echo drains the queue → re-aliased.
      b2.ops[0].canonicalHlc = create(HLCSchema, { physical: 20n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(b2)
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('a second pending batch does not write through into confirmedState', () => {
      // The detach is per-BATCH, not per-manager. speculativeState's record
      // maps are SHALLOW copies of confirmedState's, so a record no earlier
      // batch touched is still the confirmed one -- and `apply.ts` writes
      // registers IN PLACE. Batch 2 touching a record batch 1 left alone is
      // therefore the case a "detached, so we're safe" reading misses.
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      // Seed a tab into confirmedState so batch 2 mutates a PRE-EXISTING
      // record (a new one lands in the shallow-copied map and is harmless).
      const seed = setTabTileId({ originClientId: 'remote', clock: new HLCClock('remote') }, TabType.AGENT, 'tA', 'tile-CONFIRMED')
      seed.canonicalHlc = create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'remote' })
      mgr.consumeRemote(newBatch([seed]))
      expect(mgr.state.confirmedState.tabs.tA?.tileId?.value).toBe('tile-CONFIRMED')

      // Batch 1 touches a DIFFERENT record, so tabs.tA is never cloned.
      mgr.submit(newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)]))
      // Batch 2 lands while batch 1 is still pending.
      const b2 = newBatch([setTabTileId(ctx, TabType.AGENT, 'tA', 'tile-SPECULATIVE')])
      mgr.submit(b2)

      expect(mgr.state.speculativeState.tabs.tA?.tileId?.value).toBe('tile-SPECULATIVE')
      expect(mgr.state.confirmedState.tabs.tA?.tileId?.value).toBe('tile-CONFIRMED')
    })

    it('a rejected second batch leaves confirmedState unchanged', () => {
      // The data-loss half: recomputeSpeculative re-derives FROM
      // confirmedState, so a write that reached confirmedState survives the
      // rejection that was supposed to undo it -- permanently, and under the
      // local HLC, which then suppresses lower-HLC remote writes.
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const seed = setTabTileId({ originClientId: 'remote', clock: new HLCClock('remote') }, TabType.AGENT, 'tA', 'tile-CONFIRMED')
      seed.canonicalHlc = create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'remote' })
      mgr.consumeRemote(newBatch([seed]))

      mgr.submit(newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)]))
      const b2 = newBatch([setTabTileId(ctx, TabType.AGENT, 'tA', 'tile-SPECULATIVE')])
      mgr.submit(b2)
      mgr.consumeBatchRejected(b2.batchId, create(BatchRejectionSchema, {
        reason: BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH,
        offendingOpId: b2.ops[0].opId,
      }))

      expect(mgr.state.confirmedState.tabs.tA?.tileId?.value).toBe('tile-CONFIRMED')
      expect(mgr.state.speculativeState.tabs.tA?.tileId?.value).toBe('tile-CONFIRMED')
    })

    it('bootstrap leaves the alias intact when no batches are pending', () => {
      // Bootstrap rebuilds confirmedState; with empty pending, the
      // recomputeSpeculative fast-path must re-alias.
      mgr.bootstrap({
        userId: 'user',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: { physical: 0n, logical: 0n, clientId: '' },
        currentEpoch: 1n,
      })
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('explicit recomputeSpeculative call still aliases when pending is empty', () => {
      // Public API: useUserEvents calls recomputeSpeculative after
      // mutating confirmedState directly via EntityMaterialized /
      // EntityRemoved. Empty pending must yield the alias.
      mgr.recomputeSpeculative()
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })
  })

  describe('speculative canonical HLC handling', () => {
    // The hub rejects ops that arrive with canonical_hlc pre-set, so
    // submit() / recomputeSpeculative() must NOT mutate the persisted
    // op when applying speculatively. The HLC fallback is supplied as
    // a per-call override to applyOp instead.

    it('submit leaves op.canonicalHlc unset on the persisted batch', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
      // Sanity: ops are minted with clientHlc only.
      expect(batch.ops[0].clientHlc).toBeDefined()
      expect(batch.ops[0].canonicalHlc).toBeUndefined()
      mgr.submit(batch)
      // Speculative state reflects the local intent…
      expect(mgr.state.speculativeState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
      // …but the op's canonicalHlc stays unset so the wire-emit sends
      // a "client hasn't assigned canonical" payload to the hub.
      expect(batch.ops[0].canonicalHlc).toBeUndefined()
    })

    it('recomputeSpeculative does not mutate op.canonicalHlc when re-folding pending batches', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
      mgr.submit(batch)
      // A foreign remote op forces a recompute (via consumeRemote →
      // recomputeSpeculative). The pending batch's op should remain
      // canonical-less afterwards.
      const remote = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'other-node', NodeKind.SPLIT)
      remote.canonicalHlc = create(HLCSchema, { physical: 999n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(newBatch([remote]))
      expect(batch.ops[0].canonicalHlc).toBeUndefined()
      // And speculative still has the local op.
      expect(mgr.state.speculativeState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
    })

    it('consumeBatchCommitted is the only path that stamps canonicalHlc on the persisted op', () => {
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.GRID)])
      mgr.submit(batch)
      expect(batch.ops[0].canonicalHlc).toBeUndefined()
      const canonical = create(HLCSchema, { physical: 700n, logical: 0n, clientId: 'hub' })
      mgr.consumeBatchCommitted(batch.batchId, create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, { opId: batch.ops[0].opId, canonicalHlc: canonical })],
        maxHlc: canonical,
        epoch: 1n,
      }))
      // Now the persisted op carries the real canonical HLC.
      expect(batch.ops[0].canonicalHlc?.physical).toBe(700n)
      expect(batch.ops[0].canonicalHlc?.clientId).toBe('hub')
    })

    it('ops with neither clientHlc nor canonicalHlc are dropped (no-op apply)', () => {
      // Edge case: a malformed op missing both HLCs. Speculative apply
      // should silently skip it rather than throw or corrupt state.
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
      batch.ops[0].clientHlc = undefined as never
      batch.ops[0].canonicalHlc = undefined as never
      mgr.submit(batch)
      expect(mgr.state.speculativeState.nodes.n1).toBeUndefined()
    })
  })

  describe('hydrate + confirmed-mutation observer (cross-refresh checkpoint)', () => {
    /**
     * Build a HydrationPayload mirroring what loadHydrationState would produce:
     * a checkpoint UserCrdtState + op-log frames to replay. The checkpoint
     * carries one node at physical 100; the op-log replays a batch that lands
     * a second node at physical 200, advancing the watermark from T_c (100) to
     * T_now (200).
     */
    function buildPayload(): HydrationPayload {
      const state = create(UserCrdtStateSchema, {
        userId: 'user',
        nodes: { base: create(NodeRecordSchema, { nodeId: 'base' }) },
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 5n,
      })
      const deltaOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'replayed', NodeKind.GRID)
      deltaOp.canonicalHlc = create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' })
      const frame = create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([deltaOp]) } })
      // A recorded op-log holds COMPLETE batch sequences: the batch frame plus
      // the batch_end that closes it. The watermark advances only on batch_end,
      // so a fixture without one would replay the ops but leave the cursor at
      // T_c -- which is exactly the mid-sequence-drop case, not a normal log.
      const end = create(WatchUserEventSchema, {
        event: { case: 'batchEnd', value: { atHlc: create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' }) } },
      })
      return {
        state,
        frames: [frame, end],
        watermark: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 5n,
        truncated: false,
        persistedBase: { nextOpLogOrdinal: 0 },
      }
    }

    it('hydrate installs the checkpoint state and replays the op-log to T_now', () => {
      const mgr = makeMgr()
      const payload = buildPayload()
      mgr.hydrate(payload)

      // The checkpoint base node survives.
      expect(mgr.state.confirmedState.nodes.base).toBeDefined()
      // The replayed op landed on confirmedState.
      expect(mgr.state.confirmedState.nodes.replayed?.kind?.value).toBe(NodeKind.GRID)
      // Watermark advanced from T_c (100) to T_now (200) via the replay.
      expect(mgr.state.resumeWatermark?.physical).toBe(200n)
      expect(mgr.state.currentEpoch).toBe(5n)
      // isConfirmedPopulated is true after hydrate — the refresh guard flows
      // the watermark as the resume cursor.
      expect(mgr.isConfirmedPopulated()).toBe(true)
      // Speculative re-derived from the hydrated confirmed.
      expect(mgr.state.speculativeState.nodes.replayed).toBeDefined()
    })

    it('hydrate with an empty op-log keeps the watermark at T_c', () => {
      const mgr = makeMgr()
      const payload = buildPayload()
      mgr.hydrate({ ...payload, frames: [] })
      expect(mgr.state.confirmedState.nodes.base).toBeDefined()
      expect(mgr.state.resumeWatermark?.physical).toBe(100n)
    })

    it('hydrate disables recording during replay so replayed frames are not re-logged', () => {
      const mgr = makeMgr()
      const recorded: unknown[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          recorded.push(frame)
        },
        onCheckpointReset: () => {},
      })
      mgr.hydrate(buildPayload())
      // The replayed frame must NOT be recorded (it's already in the op-log).
      expect(recorded).toHaveLength(0)
    })

    it('the observer records confirmed frames after recording is enabled', () => {
      const mgr = makeMgr()
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case)
            recorded.push(frame.event.case)
        },
        onCheckpointReset: () => {},
      })

      // bootstrap must NOT be recorded (it's a checkpoint reset, not an op).
      mgr.bootstrap({
        userId: 'user',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      })
      expect(recorded).toHaveLength(0)

      // A confirmed remote batch IS recorded.
      const op = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'n1', NodeKind.LEAF)
      op.canonicalHlc = create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(newBatch([op]))
      expect(recorded).toHaveLength(1)
      expect(recorded[0]).toBe('batch')

      // A speculative submit is NOT recorded.
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      mgr.submit(newBatch([setNodeKind(ctx, 'spec', NodeKind.LEAF)]))
      expect(recorded).toHaveLength(1)
    })

    // This used to read "recording defaults to off, so an observer installed
    // before setRecording records nothing" -- a state that no longer exists.
    // Recording was gated by a separate boolean, so "hooks attached but
    // recording off" was spellable and had to be asserted; attaching IS
    // enabling now, and detaching is the only off switch. What still needs
    // pinning is that a manager with NOTHING attached records nothing, and that
    // detaching actually stops it.
    it('records nothing with no recorder attached, and stops when one detaches', () => {
      const mgr = makeMgr()
      const recorded: unknown[] = []
      const remote = (nodeId: string, at: number) => {
        const op = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, nodeId, NodeKind.LEAF)
        op.canonicalHlc = create(HLCSchema, { physical: BigInt(at), logical: 0n, clientId: 'hub' })
        return newBatch([op])
      }

      mgr.consumeRemote(remote('n1', 2))
      expect(recorded).toHaveLength(0)

      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          recorded.push(frame)
        },
        onCheckpointReset: () => {},
      })
      mgr.consumeRemote(remote('n2', 3))
      expect(recorded).toHaveLength(1)

      mgr.attachRecorder(null)
      mgr.consumeRemote(remote('n3', 4))
      expect(recorded).toHaveLength(1)
    })

    it('a delta received after hydrate folds onto the hydrated state', () => {
      const mgr = makeMgr()
      mgr.hydrate(buildPayload())
      // Watermark is at 200 after hydrate. A delta at 300 must fold on top.
      const deltaOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'post-resume', NodeKind.SPLIT)
      deltaOp.canonicalHlc = create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' })
      mgr.applyDelta(create(ResumeDeltaSchema, {
        frames: [create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([deltaOp]) } })],
        maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 5n,
      }))
      expect(mgr.state.confirmedState.nodes['post-resume']?.kind?.value).toBe(NodeKind.SPLIT)
      expect(mgr.state.resumeWatermark?.physical).toBe(300n)
      // The hydrated base + replayed nodes are still present.
      expect(mgr.state.confirmedState.nodes.base).toBeDefined()
      expect(mgr.state.confirmedState.nodes.replayed).toBeDefined()
    })

    it('consumeBatchCommitted then the echoed consumeRemote record the own batch ONCE (no double-record)', () => {
      // The hub broadcasts every committed batch to ALL subscribers including
      // the originator, so an originating client receives its own batch again
      // via consumeRemote. Without the dedup key, the own batch would land in
      // the op-log twice. This test pins the single-record contract.
      const mgr = makeMgr()
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case === 'batch')
            recorded.push(frame.event.value.batchId)
        },
        onCheckpointReset: () => {},
      })

      // Seed a pending batch, then commit it (the SubmitOps echo path).
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const op = setNodeKind(ctx, 'ownNode', NodeKind.LEAF)
      const batch = newBatch([op], 'own-batch-1')
      mgr.submit(batch)
      mgr.consumeBatchCommitted('own-batch-1', create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, {
          opId: op.opId,
          canonicalHlc: create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' }),
        })],
        maxHlc: create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' }),
        epoch: 1n,
      }))
      expect(recorded).toEqual(['own-batch-1'])

      // The hub now broadcasts the same batch back (originator echo). The dedup
      // key must suppress the second recording — the op-log already holds it.
      const echoOp = setNodeKind(ctx, 'ownNode', NodeKind.LEAF)
      echoOp.canonicalHlc = create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(newBatch([echoOp], 'own-batch-1'))
      expect(recorded).toEqual(['own-batch-1'])

      // A genuinely remote batch (different batchId) records normally.
      const remoteOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'remoteNode', NodeKind.GRID)
      remoteOp.canonicalHlc = create(HLCSchema, { physical: 3n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(newBatch([remoteOp], 'remote-batch-1'))
      expect(recorded).toEqual(['own-batch-1', 'remote-batch-1'])
    })

    it('does not re-record an own batch the resume tail re-ships', () => {
      // A socket drop can land between the SubmitOps response (which
      // consumeBatchCommitted already logged) and the hub's broadcast of the
      // same batch. The reconnect's resume tail then re-ships it -- the hub
      // applies no origin filter to a tail -- so without the same dedup
      // consumeRemote applies, the op-log ends up holding two frames for one
      // batch_id, and every cold reload replays the batch twice.
      const mgr = makeMgr()
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case === 'batch')
            recorded.push(frame.event.value.batchId)
        },
        onCheckpointReset: () => {},
      })

      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const op = setNodeKind(ctx, 'ownNode', NodeKind.LEAF)
      mgr.submit(newBatch([op], 'own-batch-1'))
      mgr.consumeBatchCommitted('own-batch-1', create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, {
          opId: op.opId,
          canonicalHlc: create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' }),
        })],
        maxHlc: create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' }),
        epoch: 1n,
      }))
      expect(recorded).toEqual(['own-batch-1'])

      // The resume tail carries the lost echo AND a genuinely remote batch.
      const echoOp = setNodeKind(ctx, 'ownNode', NodeKind.LEAF)
      echoOp.canonicalHlc = create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'hub' })
      const remoteOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'remoteNode', NodeKind.GRID)
      remoteOp.canonicalHlc = create(HLCSchema, { physical: 3n, logical: 0n, clientId: 'hub' })
      mgr.applyDelta(create(ResumeDeltaSchema, {
        frames: [
          create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([echoOp], 'own-batch-1') } }),
          create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([remoteOp], 'remote-batch-1') } }),
        ],
        maxHlc: create(HLCSchema, { physical: 3n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      }))

      // The own batch is logged once in total; the peer's is logged normally.
      expect(recorded).toEqual(['own-batch-1', 'remote-batch-1'])
      // Suppressing the RECORD never suppresses the APPLY: both batches landed.
      expect(mgr.state.confirmedState.nodes.ownNode?.kind?.value).toBe(NodeKind.LEAF)
      expect(mgr.state.confirmedState.nodes.remoteNode?.kind?.value).toBe(NodeKind.GRID)
    })

    it('records a tail-only batch that has no own-echo marker', () => {
      // The other half of the dedup: a resume tail is mostly peer traffic and
      // must still be logged, or a cold reload would replay a truncated log.
      const mgr = makeMgr()
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case)
            recorded.push(frame.event.case)
        },
        onCheckpointReset: () => {},
      })

      const remoteOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'peerNode', NodeKind.LEAF)
      remoteOp.canonicalHlc = create(HLCSchema, { physical: 5n, logical: 0n, clientId: 'hub' })
      mgr.applyDelta(create(ResumeDeltaSchema, {
        frames: [
          create(WatchUserEventSchema, { event: { case: 'batch', value: newBatch([remoteOp], 'peer-batch-1') } }),
          create(WatchUserEventSchema, {
            event: { case: 'batchEnd', value: { atHlc: create(HLCSchema, { physical: 5n, logical: 0n, clientId: 'hub' }) } },
          }),
        ],
        maxHlc: create(HLCSchema, { physical: 5n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      }))

      expect(recorded).toEqual(['batch', 'batchEnd'])
    })

    it('bounds the own-echo dedup set, evicting the oldest ids first', () => {
      // bootstrap and hydrate are the only wholesale clears, and a client that
      // keeps resuming runs neither -- so an id whose echo genuinely never
      // arrives would otherwise be resident for the life of the page. Past the
      // cap the oldest go first, and an evicted id costs exactly one duplicate
      // op-log frame (harmless: re-applying a batch at the same canonical HLC
      // fails shouldWrite's strict-greater compare).
      const mgr = makeMgr()
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case === 'batch')
            recorded.push(frame.event.value.batchId)
        },
        onCheckpointReset: () => {},
      })

      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const hlcAt = (n: number) => create(HLCSchema, { physical: BigInt(n), logical: 0n, clientId: 'hub' })
      // One more commit than the cap holds, with NO echo arriving for any.
      const overflow = MAX_OWN_ECHO_BATCH_IDS + 1
      for (let i = 0; i < overflow; i++) {
        const op = setNodeKind(ctx, `n${i}`, NodeKind.LEAF)
        mgr.submit(newBatch([op], `b${i}`))
        mgr.consumeBatchCommitted(`b${i}`, create(BatchCommittedSchema, {
          committed: [create(CommittedOpSchema, { opId: op.opId, canonicalHlc: hlcAt(i + 1) })],
          maxHlc: hlcAt(i + 1),
          epoch: 1n,
        }))
      }
      expect(recorded).toHaveLength(overflow)
      recorded.length = 0

      const echoOf = (i: number) => {
        const op = setNodeKind(ctx, `n${i}`, NodeKind.LEAF)
        op.canonicalHlc = hlcAt(i + 1)
        return newBatch([op], `b${i}`)
      }
      // b0 was evicted as the oldest, so its late echo records a second time.
      mgr.consumeRemote(echoOf(0))
      expect(recorded).toEqual(['b0'])
      // Everything still resident -- the new oldest and the newest -- dedups.
      mgr.consumeRemote(echoOf(1))
      mgr.consumeRemote(echoOf(overflow - 1))
      expect(recorded).toEqual(['b0'])
    })

    it('advances the resume watermark ONLY at the batch boundary', () => {
      // Every frame of one batch carries the SAME at_hlc (the batch's last-op
      // HLC). Advancing per frame moved the cursor to the batch's max as soon as
      // the FIRST frame landed, so a socket drop mid-sequence left the persisted
      // cursor above ops that were never applied -- and the resume scan is
      // strictly-greater, so the hub would never re-send them.
      const mgr = makeMgr()
      mgr.bootstrap({
        userId: 'user',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      })
      const atHlc = create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' })

      // Leading materialized frame of the batch's sequence: applied, but the
      // cursor must NOT move -- the batch's own ops have not arrived yet.
      mgr.consumeEntityMaterialized(create(EntityMaterializedSchema, {
        atHlc,
        entity: { case: 'node', value: create(NodeRecordSchema, { nodeId: 'crossed-in' }) },
      }))
      expect(mgr.state.confirmedState.nodes['crossed-in']).toBeDefined()
      expect(mgr.state.resumeWatermark?.physical).toBe(100n)

      // The batch frame lands. Still no advance: a trailing entity_removed for
      // the same batch may yet follow, and losing it would leave a redacted
      // entity rendered with the cursor already past the batch.
      const op = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'edited', NodeKind.GRID)
      op.canonicalHlc = atHlc
      mgr.consumeRemote(newBatch([op], 'seq-batch'))
      expect(mgr.state.confirmedState.nodes.edited?.kind?.value).toBe(NodeKind.GRID)
      expect(mgr.state.resumeWatermark?.physical).toBe(100n)

      // The boundary closes the sequence: now, and only now, the cursor moves.
      mgr.consumeBatchEnd(atHlc)
      expect(mgr.state.resumeWatermark?.physical).toBe(300n)
    })

    it('does not advance the resume watermark on the SubmitOps echo', () => {
      // The echo arrives over the Connect RPC, a transport wholly separate from
      // the WS the hub's frames ride, so its ordering against a peer's in-flight
      // batch is unconstrained. Advancing here moved the cursor past ops still
      // sitting unread in the socket buffer; a drop then stranded them above a
      // strictly-greater cursor forever. The hub does not suppress self-echo, so
      // the batch's own batch_end still advances the cursor -- just at the
      // boundary, where it is safe.
      const mgr = makeMgr()
      mgr.bootstrap({
        userId: 'user',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      })

      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const op = setNodeKind(ctx, 'ownNode', NodeKind.LEAF)
      mgr.submit(newBatch([op], 'own-batch'))
      mgr.consumeBatchCommitted('own-batch', create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, {
          opId: op.opId,
          canonicalHlc: create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'hub' }),
        })],
        maxHlc: create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'hub' }),
        epoch: 1n,
      }))

      // The ops ARE applied to confirmedState -- only the cursor is withheld.
      expect(mgr.state.confirmedState.nodes.ownNode?.kind?.value).toBe(NodeKind.LEAF)
      expect(mgr.state.resumeWatermark?.physical).toBe(100n)

      // The hub's broadcast of the same batch closes it, and the cursor moves.
      mgr.consumeBatchEnd(create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'hub' }))
      expect(mgr.state.resumeWatermark?.physical).toBe(500n)
    })

    it('holds the cursor behind a peer batch that lost the race with the echo', () => {
      // The concrete divergence the rule above prevents. Peer batch P commits at
      // 400 and is queued on this client's socket; this client's own batch
      // commits at 500 and its SubmitOps response is processed first. If the
      // echo advanced the cursor to 500, a drop before P was read would leave a
      // persisted cursor above P -- and ListUserOpBatchesAfter is
      // strictly-greater, so P would never be re-sent.
      const mgr = makeMgr()
      mgr.bootstrap({
        userId: 'user',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: create(HLCSchema, { physical: 100n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      })

      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const own = setNodeKind(ctx, 'ownNode', NodeKind.LEAF)
      mgr.submit(newBatch([own], 'own-batch'))
      mgr.consumeBatchCommitted('own-batch', create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, {
          opId: own.opId,
          canonicalHlc: create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'hub' }),
        })],
        maxHlc: create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'hub' }),
        epoch: 1n,
      }))

      // The socket drops here. The cursor must still be behind the peer's
      // batch at 400, so the resume re-requests it.
      expect(mgr.state.resumeWatermark?.physical).toBeLessThan(400n)
    })

    it('records each batch of a MULTI-batch submit exactly once across their echoes', () => {
      // useOpsSubmitter aggregates over a 16ms window, so one SubmitOps commonly
      // carries several batches and its response drives one
      // consumeBatchCommitted per batch. A single-slot dedup key kept only the
      // LAST id, so submitting {A, B} left the slot at B and A's echo recorded A
      // a SECOND time -- every multi-batch submit double-logged all but its
      // final batch, tripping the checkpoint threshold early and lengthening
      // every cold-reload replay.
      const mgr = makeMgr()
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case === 'batch')
            recorded.push(frame.event.value.batchId)
        },
        onCheckpointReset: () => {},
      })

      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      const opA = setNodeKind(ctx, 'nodeA', NodeKind.LEAF)
      const opB = setNodeKind(ctx, 'nodeB', NodeKind.LEAF)
      mgr.submit(newBatch([opA], 'multi-a'))
      mgr.submit(newBatch([opB], 'multi-b'))

      const commit = (opId: string, physical: bigint) => create(BatchCommittedSchema, {
        committed: [create(CommittedOpSchema, {
          opId,
          canonicalHlc: create(HLCSchema, { physical, logical: 0n, clientId: 'hub' }),
        })],
        maxHlc: create(HLCSchema, { physical, logical: 0n, clientId: 'hub' }),
        epoch: 1n,
      })
      mgr.consumeBatchCommitted('multi-a', commit(opA.opId, 2n))
      mgr.consumeBatchCommitted('multi-b', commit(opB.opId, 3n))
      expect(recorded).toEqual(['multi-a', 'multi-b'])

      // Both echoes arrive; NEITHER may be recorded again.
      const echo = (nodeId: string, batchId: string, physical: bigint) => {
        const op = setNodeKind(ctx, nodeId, NodeKind.LEAF)
        op.canonicalHlc = create(HLCSchema, { physical, logical: 0n, clientId: 'hub' })
        return newBatch([op], batchId)
      }
      mgr.consumeRemote(echo('nodeA', 'multi-a', 2n))
      mgr.consumeRemote(echo('nodeB', 'multi-b', 3n))
      expect(recorded).toEqual(['multi-a', 'multi-b'])
    })

    it('bootstrap fires the checkpoint-reset observer and clears pending batches', () => {
      // A bootstrap wholesale-replaces confirmedState from a fresh snapshot,
      // so the persisted checkpoint+op-log MUST re-base. bootstrap fires the
      // checkpoint-reset observer (wired by useCrdtRuntime to rewrite the
      // checkpoint) and clears any in-flight speculative edits (the snapshot
      // supersedes them).
      const mgr = makeMgr()
      let resets = 0
      mgr.attachRecorder({
        record: () => {},
        onCheckpointReset: () => {
          resets++
        },
      })

      // Seed a pending batch against the pre-bootstrap state.
      const ctx = { originClientId: 'clientA', clock: mgr.clock }
      mgr.submit(newBatch([setNodeKind(ctx, 'speculative', NodeKind.LEAF)], 'pending-1'))
      expect(mgr.state.pendingBatches).toHaveLength(1)

      mgr.bootstrap({
        userId: 'user',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        maxHlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'hub' }),
        currentEpoch: 1n,
      })
      expect(resets).toBe(1)
      // The pending batch is cleared — the snapshot is authoritative.
      expect(mgr.state.pendingBatches).toHaveLength(0)
    })

    it('hydrate clears pending batches and the own-batch dedup key', () => {
      // A cold reload dies the old JS heap, so any in-flight speculative edits
      // and the dedup key from the prior session are gone. hydrate resets them
      // so a freshly-echoed batch isn't suppressed by a stale id.
      const mgr = makeMgr()
      mgr.submit(newBatch([setNodeKind({ originClientId: 'clientA', clock: mgr.clock }, 's', NodeKind.LEAF)], 'stale-1'))
      expect(mgr.state.pendingBatches).toHaveLength(1)

      mgr.hydrate(buildPayload())
      expect(mgr.state.pendingBatches).toHaveLength(0)

      // After hydrate, a consumeBatchCommitted for the stale id records (the
      // dedup key was cleared) — though in practice the prior session's pending
      // batch can't commit post-reload. This pins the "hydrate is a fresh
      // lineage" contract.
      const recorded: string[] = []
      mgr.attachRecorder({
        record: (frame: WatchUserEvent) => {
          if (frame.event.case === 'batch')
            recorded.push(frame.event.value.batchId)
        },
        onCheckpointReset: () => {},
      })
      // consumeRemote of a remote batch records (dedup key is null post-hydrate).
      const remoteOp = setNodeKind({ originClientId: 'other', clock: new HLCClock('other') }, 'n', NodeKind.GRID)
      remoteOp.canonicalHlc = create(HLCSchema, { physical: 250n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(newBatch([remoteOp], 'fresh-remote-1'))
      expect(recorded).toEqual(['fresh-remote-1'])
    })
  })
})

// Client-side tombstone GC. Delta-resume removed the only one the client ever
// had: applyOp installs a tombstone SHELL rather than deleting, and
// materializedFromState omits tombstoned records, so every full-snapshot bootstrap
// used to reset confirmedState to a tombstone-free base. A client that keeps
// resuming never bootstraps, so from that point every closed tab, tile and
// window stayed in confirmedState -- and in the serialized checkpoint blob --
// forever. The hub never propagates its own pruning (PruneTombstonesAtOrBelow
// edits server state and emits no ops).
describe('pruneTombstonesAtOrBelow', () => {
  function stateWithTombstones(): UserCrdtState {
    return create(UserCrdtStateSchema, {
      userId: 'u',
      nodes: {
        // No tombstoneAt at all -- a live record.
        live: {},
        old: { tombstoneAt: { physical: 5n, logical: 0n, clientId: 'hub' } },
        atFloor: { tombstoneAt: { physical: 10n, logical: 0n, clientId: 'hub' } },
        recent: { tombstoneAt: { physical: 20n, logical: 0n, clientId: 'hub' } },
      },
    })
  }

  it('drops tombstones at or below the floor and keeps everything else', () => {
    const state = stateWithTombstones()
    const pruned = pruneTombstonesAtOrBelow(state, create(HLCSchema, { physical: 10n, logical: 0n, clientId: 'hub' }))

    expect(pruned).toBe(2)
    // AT the floor goes too -- the hub's rule is "at or below".
    expect(Object.keys(state.nodes).sort()).toEqual(['live', 'recent'])
  })

  it('is a no-op without a floor, so an un-watermarked client never prunes', () => {
    const state = stateWithTombstones()
    expect(pruneTombstonesAtOrBelow(state, undefined)).toBe(0)
    expect(pruneTombstonesAtOrBelow(state, create(HLCSchema, {}))).toBe(0)
    expect(Object.keys(state.nodes)).toHaveLength(4)
  })

  it('never drops a live record, whatever the floor', () => {
    const state = stateWithTombstones()
    pruneTombstonesAtOrBelow(state, create(HLCSchema, { physical: 1_000n, logical: 0n, clientId: 'hub' }))
    expect(Object.keys(state.nodes)).toEqual(['live'])
  })

  it('keeps a pinned shell even when it is below the floor', () => {
    const state = stateWithTombstones()
    const pruned = pruneTombstonesAtOrBelow(
      state,
      create(HLCSchema, { physical: 1_000n, logical: 0n, clientId: 'hub' }),
      new Set(['node:old']),
    )
    expect(pruned).toBe(2)
    expect(Object.keys(state.nodes).sort()).toEqual(['live', 'old'])
  })

  it('pins by kind, so an id shared across maps is not over-pinned', () => {
    const state = create(UserCrdtStateSchema, {
      userId: 'u',
      nodes: { shared: { tombstoneAt: { physical: 5n, logical: 0n, clientId: 'hub' } } },
      tabs: { shared: { tombstoneAt: { physical: 5n, logical: 0n, clientId: 'hub' } } },
    })
    pruneTombstonesAtOrBelow(
      state,
      create(HLCSchema, { physical: 1_000n, logical: 0n, clientId: 'hub' }),
      new Set(['node:shared']),
    )
    expect(Object.keys(state.nodes)).toEqual(['shared'])
    expect(Object.keys(state.tabs)).toEqual([])
  })
})

// A tombstone shell is the ONLY thing that makes apply.ts drop a later register
// write, and compactTombstones re-derives speculativeState right after pruning.
// Without pinning, that replay lands an unconfirmed local batch on the pruned
// entity and `applyOp` lazily re-creates it as a LIVE record -- a ghost tab in
// the tab bar that the checkpoint then serializes to disk.
describe('pendingOpsManager compactTombstones with unconfirmed local ops', () => {
  it('does not resurrect an entity a pending batch still targets', () => {
    const mgr = makeMgr()
    const ctx = { originClientId: 'clientA', clock: mgr.clock }

    // A local drag that has not been confirmed yet.
    mgr.submit(newBatch([setTabTileId(ctx, TabType.AGENT, 'tA', 'tile-b')]))
    expect(mgr.state.pendingBatches).toHaveLength(1)

    // A peer closes the tab, and batch_end moves the watermark past the
    // tombstone so it becomes eligible for pruning.
    const peerTombstone = tombstoneTab(
      { originClientId: 'peer', clock: new HLCClock('peer') },
      TabType.AGENT,
      'tA',
    )
    peerTombstone.canonicalHlc = create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'peer' })
    mgr.consumeRemote(create(OpBatchSchema, { batchId: 'peer-batch', ops: [peerTombstone] }))
    mgr.consumeBatchEnd(create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'peer' }))

    expect(mgr.state.confirmedState.tabs.tA?.tombstoneAt).toBeDefined()

    mgr.compactTombstones()

    // The shell survives, so the pending write is still suppressed and the
    // speculative replay cannot re-create the tab as a live record.
    expect(mgr.state.confirmedState.tabs.tA).toBeDefined()
    expect(mgr.state.speculativeState.tabs.tA?.tombstoneAt).toBeDefined()
    expect(mgr.state.speculativeState.tabs.tA?.tileId).not.toBe('tile-b')
  })

  // The SubmitOps echo rides the Connect RPC while the epoch also advances over
  // the WS, with no ordering between the two transports and no in-flight guard
  // in useOpsSubmitter. A response minted under the OLD epoch can therefore land
  // after a bootstrap installed the new one; decideResume requires an EXACT
  // epoch match, so regressing it costs a full projection snapshot on the next
  // reconnect.
  it('never regresses currentEpoch from a late SubmitOps echo', () => {
    const mgr = makeMgr()
    mgr.state.currentEpoch = 6n

    mgr.consumeBatchCommitted('no-such-batch', create(BatchCommittedSchema, {
      epoch: 5n,
    }))
    expect(mgr.state.currentEpoch).toBe(6n)

    mgr.consumeBatchCommitted('no-such-batch', create(BatchCommittedSchema, {
      epoch: 7n,
    }))
    expect(mgr.state.currentEpoch).toBe(7n)
  })

  it('still prunes once the pending batch is gone', () => {
    const mgr = makeMgr()
    const peerTombstone = tombstoneTab(
      { originClientId: 'peer', clock: new HLCClock('peer') },
      TabType.AGENT,
      'tA',
    )
    peerTombstone.canonicalHlc = create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'peer' })
    mgr.consumeRemote(create(OpBatchSchema, { batchId: 'peer-batch', ops: [peerTombstone] }))
    mgr.consumeBatchEnd(create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'peer' }))

    expect(mgr.compactTombstones()).toBe(1)
    expect(mgr.state.confirmedState.tabs.tA).toBeUndefined()
  })
})

// The two batch-boundary paths -- consumeBatchEnd (live) and applyFrames'
// `batchEnd` arm (cold-reload replay / resume tail) -- are one rule now, and
// these pin the half where they had ALREADY drifted: consumeBatchEnd refused a
// missing at_hlc while applyFrames observed and advanced unguarded, so a
// malformed frame moved the resume cursor on one path and not the other.
describe('batch-end boundary', () => {
  it('advances the watermark identically on the live and replay paths', () => {
    const atHlc = create(HLCSchema, { physical: 42n, logical: 3n, clientId: 'hub' })

    const live = makeMgr()
    live.consumeBatchEnd(atHlc)

    // The replay path reaches the same arm through hydrate(), which is how a
    // cold reload re-applies the persisted op-log.
    const replay = makeMgr()
    replay.hydrate({
      state: create(UserCrdtStateSchema, { userId: 'u' }),
      frames: [create(WatchUserEventSchema, { event: { case: 'batchEnd', value: { atHlc } } })],
      watermark: { physical: 1n, logical: 0n, clientId: 'hub' },
      currentEpoch: 1n,
      truncated: false,
      persistedBase: { nextOpLogOrdinal: 0 },
    })

    expect(live.state.resumeWatermark?.physical).toBe(42n)
    expect(replay.state.resumeWatermark?.physical).toBe(42n)
    expect(replay.state.resumeWatermark?.logical).toBe(live.state.resumeWatermark?.logical)
  })

  it('refuses a batch_end with no at_hlc on BOTH paths', () => {
    const live = makeMgr()
    live.consumeBatchEnd(undefined)

    // Hydrate pins the checkpoint watermark first, so the assertion is that the
    // malformed frame does not move it OFF that pinned value.
    const replay = makeMgr()
    replay.hydrate({
      state: create(UserCrdtStateSchema, { userId: 'u' }),
      frames: [create(WatchUserEventSchema, { event: { case: 'batchEnd', value: {} } })],
      watermark: { physical: 7n, logical: 0n, clientId: 'hub' },
      currentEpoch: 1n,
      truncated: false,
      persistedBase: { nextOpLogOrdinal: 0 },
    })

    // A frame naming no boundary must not move the cursor: advancing on one
    // would skip whatever the hub ships strictly after it, permanently.
    expect(live.state.resumeWatermark).toBeUndefined()
    expect(replay.state.resumeWatermark?.physical).toBe(7n)
  })
})

describe('pendingOpsManager compactTombstones', () => {
  it('prunes shells below the resume cursor and re-derives the projection base', () => {
    const mgr = makeMgr()
    mgr.state.confirmedState = create(UserCrdtStateSchema, {
      userId: 'u',
      nodes: {
        live: {},
        gone: { tombstoneAt: { physical: 5n, logical: 0n, clientId: 'hub' } },
      },
    })
    mgr.state.resumeWatermark = create(HLCSchema, { physical: 9n, logical: 0n, clientId: 'hub' })

    expect(mgr.compactTombstones()).toBe(1)
    expect(Object.keys(mgr.state.confirmedState.nodes)).toEqual(['live'])
    // speculativeState aliases confirmedState with no pending batches, so the
    // prune must be visible through the projection source too.
    expect(Object.keys(mgr.state.speculativeState.nodes)).toEqual(['live'])
  })

  it('prunes nothing before the client has a resume cursor', () => {
    const mgr = makeMgr()
    mgr.state.confirmedState = create(UserCrdtStateSchema, {
      userId: 'u',
      nodes: { gone: { tombstoneAt: { physical: 5n, logical: 0n, clientId: 'hub' } } },
    })
    mgr.state.resumeWatermark = undefined

    expect(mgr.compactTombstones()).toBe(0)
    expect(Object.keys(mgr.state.confirmedState.nodes)).toEqual(['gone'])
  })
})

// The pin's fail-safe direction. An op body this build cannot map to an entity
// means the pin set cannot be enumerated -- and an unpinned shell is exactly
// what lets recomputeSpeculative resurrect a tombstoned record. So the whole
// prune pass is skipped rather than run with a partial pin set.
describe('pendingOpsManager compactTombstones with an unrecognized op', () => {
  it('prunes nothing rather than pruning with a partial pin set', () => {
    const mgr = makeMgr()
    const peerTombstone = tombstoneTab(
      { originClientId: 'peer', clock: new HLCClock('peer') },
      TabType.AGENT,
      'tA',
    )
    peerTombstone.canonicalHlc = create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'peer' })
    mgr.consumeRemote(create(OpBatchSchema, { batchId: 'peer-batch', ops: [peerTombstone] }))
    mgr.consumeBatchEnd(create(HLCSchema, { physical: 500n, logical: 0n, clientId: 'peer' }))

    // A pending batch carrying a body from a hypothetical newer build.
    mgr.state.pendingBatches.push({
      batchId: 'from-the-future',
      ops: [{ opId: 'x', body: { case: 'someOpFromANewerBuild', value: {} } }],
    } as never)

    expect(mgr.compactTombstones()).toBe(0)
    expect(mgr.state.confirmedState.tabs.tA).toBeDefined()
  })
})
