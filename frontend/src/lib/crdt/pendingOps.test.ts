import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { HLCSchema, NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import {
  BatchCommittedSchema,
  BatchRejectionSchema,
  CommittedOpSchema,
  EntityMaterializedSchema,
  EntityRemovedSchema,
  TabIdentSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { HLCClock } from './hlc'
import { newBatch, setNodeKind, setTabTileId, tombstoneTab } from './ops'
import { PendingOpsManager } from './pendingOps'

function makeMgr(notify?: () => void) {
  const clock = new HLCClock('clientA')
  return new PendingOpsManager('org', clock, notify)
}

describe('pendingOpsManager', () => {
  let mgr: PendingOpsManager

  beforeEach(() => {
    mgr = makeMgr()
  })

  it('submit applies the batch speculatively (canonical_hlc fallback)', () => {
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
    const op = setNodeKind({ orgId: 'org', originClientId: 'other', clock: new HLCClock('other') }, 'remote-node', NodeKind.SPLIT)
    op.canonicalHlc = create(HLCSchema, { physical: 200n, logical: 0n, clientId: 'hub' })
    mgr.consumeRemote(newBatch([op]))
    expect(mgr.state.confirmedState.nodes['remote-node']?.kind?.value).toBe(NodeKind.SPLIT)
    expect(mgr.state.speculativeState.nodes['remote-node']?.kind?.value).toBe(NodeKind.SPLIT)
  })

  it('consumeBatchCommitted replaces clientHlc with canonicalHlc and applies to confirmed', () => {
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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

  it('consumeBatchRejected drops the batch and reports retryable=false for non-retryable reasons', () => {
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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

  it('consumeBatchRejected reports retryable=true for unknown reasons (default catch-all)', () => {
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
    const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
    mgr.submit(batch)
    const result = mgr.consumeBatchRejected(batch.batchId, create(BatchRejectionSchema, {
      reason: 0, // UNSPECIFIED — represents transport-failure / unknown
      offendingOpId: '',
    }))
    expect(result.retryable).toBe(true)
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
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: m.clock }
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
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
    const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
      const op = setNodeKind({ orgId: 'org', originClientId: 'remote', clock: new HLCClock('remote') }, 'r1', NodeKind.SPLIT)
      op.canonicalHlc = create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'remote' })
      mgr.consumeRemote(newBatch([op]))
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
      // And both see the remote change (since they're literally the same proto).
      expect(mgr.state.speculativeState.nodes.r1?.kind?.value).toBe(NodeKind.SPLIT)
    })

    it('submit detaches the alias before applying the local op', () => {
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n3', NodeKind.LEAF)])
      mgr.submit(batch)
      // Use an unknown enum value (out of the BatchRejectionReason
      // range) so consumeBatchRejected classifies it as retryable —
      // this test only cares about the alias re-establishment, not
      // the rejection reason.
      const rejection = create(BatchRejectionSchema, {

        reason: 99 as any,
        offendingOpId: batch.ops[0].opId,
      })
      mgr.consumeBatchRejected(batch.batchId, rejection)
      expect(mgr.state.pendingBatches.length).toBe(0)
      expect(mgr.state.speculativeState).toBe(mgr.state.confirmedState)
    })

    it('alias stays detached while multiple batches are pending, then re-aliases when all drain', () => {
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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

    it('bootstrap leaves the alias intact when no batches are pending', () => {
      // Bootstrap rebuilds confirmedState; with empty pending, the
      // recomputeSpeculative fast-path must re-alias.
      mgr.bootstrap({
        orgId: 'org',
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
      // Public API: useOrgEvents calls recomputeSpeculative after
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
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
      mgr.submit(batch)
      // A foreign remote op forces a recompute (via consumeRemote →
      // recomputeSpeculative). The pending batch's op should remain
      // canonical-less afterwards.
      const remote = setNodeKind({ orgId: 'org', originClientId: 'other', clock: new HLCClock('other') }, 'other-node', NodeKind.SPLIT)
      remote.canonicalHlc = create(HLCSchema, { physical: 999n, logical: 0n, clientId: 'hub' })
      mgr.consumeRemote(newBatch([remote]))
      expect(batch.ops[0].canonicalHlc).toBeUndefined()
      // And speculative still has the local op.
      expect(mgr.state.speculativeState.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
    })

    it('consumeBatchCommitted is the only path that stamps canonicalHlc on the persisted op', () => {
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
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
      const ctx = { orgId: 'org', originClientId: 'clientA', clock: mgr.clock }
      const batch = newBatch([setNodeKind(ctx, 'n1', NodeKind.LEAF)])
      batch.ops[0].clientHlc = undefined as never
      batch.ops[0].canonicalHlc = undefined as never
      mgr.submit(batch)
      expect(mgr.state.speculativeState.nodes.n1).toBeUndefined()
    })
  })
})
