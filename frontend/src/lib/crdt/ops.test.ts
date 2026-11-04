import type { CrdtOp } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { CrdtOpSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { HLCClock, hlcCmp } from './hlc'
import {
  generateId,
  newBatch,
  opHasIdentity,
  opTarget,
  opTargetKey,
  reviveTab,
  selectsNoRegister,
  setFloatingHeight,
  setFloatingRootNodeId,
  setFloatingWidth,
  setFloatingWorkspaceId,
  setFloatingX,
  setFloatingY,
  setNodeKind,
  setNodeParentId,
  setNodePosition,
  setNodeRatios,
  setNodeRows,
  setTabPosition,
  setTabTileId,
  setTabWorkerId,
  tombstoneFloatingWindow,
  tombstoneNode,
  tombstoneTab,
} from './ops'

function newCtx(clientId = 'cli-A') {
  return {
    originClientId: clientId,
    clock: new HLCClock(clientId),
  }
}

describe('generateId', () => {
  it('mints 48-character alphanumeric ids matching the Go util/id.Generate alphabet', () => {
    const id = generateId()
    expect(id).toHaveLength(48)
    expect(/^[a-z0-9]+$/i.test(id)).toBe(true)
  })
  it('mints distinct ids across calls', () => {
    const ids = new Set(Array.from({ length: 100 }, () => generateId()))
    expect(ids.size).toBe(100)
  })
})

describe('op envelope', () => {
  it('stamps originClientId, and a fresh op_id + client_hlc', () => {
    const ctx = newCtx()
    const op = setNodeKind(ctx, 'node-1', 1)
    expect(op.originClientId).toBe('cli-A')
    expect(op.opId).toHaveLength(48)
    expect(op.clientHlc?.clientId).toBe('cli-A')
    // canonical_hlc is hub-assigned; the builder leaves it as the
    // default zero value.
    expect(op.canonicalHlc?.physical).toBeFalsy()
  })

  it('produces a fresh op_id per op even at the same wall clock', () => {
    const ctx = newCtx()
    const a = setNodeKind(ctx, 'n', 1)
    const b = setNodeKind(ctx, 'n', 1)
    expect(a.opId).not.toBe(b.opId)
  })

  it('ticks the HLC clock once per op', () => {
    const ctx = newCtx()
    const a = setNodeKind(ctx, 'n', 1)
    const b = setNodeKind(ctx, 'n', 1)
    // HLCClock.tick() resets `logical` to 0 whenever the wall clock
    // advances, so comparing `logical` alone is flaky on fast machines
    // that span a millisecond between the two calls. Compare the full
    // (physical, logical, clientId) pair instead: strictly increasing
    // is the actual contract.
    expect(hlcCmp(a.clientHlc, b.clientHlc)).toBeLessThan(0)
  })
})

describe('node op builders', () => {
  it('encode the single-register field discriminator', () => {
    const ctx = newCtx()
    expect(setNodeParentId(ctx, 'n', 'p').body.case).toBe('setNodeRegister')
    expect((setNodeParentId(ctx, 'n', 'p').body.value as { field: { case: string } }).field.case).toBe('parentId')
    expect((setNodePosition(ctx, 'n', 'V').body.value as { field: { case: string } }).field.case).toBe('position')
    expect((setNodeRatios(ctx, 'n', [0.5, 0.5]).body.value as { field: { case: string } }).field.case).toBe('ratios')
    expect((setNodeRows(ctx, 'n', 3).body.value as { field: { case: string } }).field.case).toBe('rows')
  })

  it('encodes the tombstone variant in tombstoneNode', () => {
    const ctx = newCtx()
    const op = tombstoneNode(ctx, 'n')
    expect(op.body.case).toBe('tombstoneNode')
    expect((op.body.value as { nodeId: string }).nodeId).toBe('n')
  })
})

describe('tab op builders', () => {
  it('attach the tab_type discriminator to every register', () => {
    const ctx = newCtx()
    const tt = TabType.AGENT
    const tile = setTabTileId(ctx, tt, 'tab-1', 'tile-1')
    expect(tile.body.case).toBe('setTabRegister')
    if (tile.body.case !== 'setTabRegister')
      return
    expect(tile.body.value.tab).toMatchObject({ tabType: tt, tabId: 'tab-1' })
    expect(tile.body.value.field.case).toBe('tileId')

    expect((setTabPosition(ctx, tt, 't', 'V').body.value as { field: { case: string } }).field.case).toBe('position')
    expect((setTabWorkerId(ctx, tt, 't', 'w').body.value as { field: { case: string } }).field.case).toBe('workerId')
  })

  it('includes tab_type in tombstoneTab so the validator can confirm uniqueness', () => {
    const ctx = newCtx()
    const op = tombstoneTab(ctx, TabType.TERMINAL, 'tab-x')
    expect(op.body.case).toBe('tombstoneTab')
    if (op.body.case !== 'tombstoneTab')
      return
    expect(op.body.value.tab).toMatchObject({ tabType: TabType.TERMINAL, tabId: 'tab-x' })
  })
})

describe('floating window op builders', () => {
  it('encode each register as its own oneof case', () => {
    const ctx = newCtx()
    const w = 'win-1'
    expect((setFloatingWorkspaceId(ctx, w, 'ws').body.value as { field: { case: string } }).field.case).toBe('workspaceId')
    expect((setFloatingX(ctx, w, 100).body.value as { field: { case: string } }).field.case).toBe('x')
    expect((setFloatingY(ctx, w, 100).body.value as { field: { case: string } }).field.case).toBe('y')
    expect((setFloatingWidth(ctx, w, 300).body.value as { field: { case: string } }).field.case).toBe('width')
    expect((setFloatingHeight(ctx, w, 200).body.value as { field: { case: string } }).field.case).toBe('height')
    expect((setFloatingRootNodeId(ctx, w, 'r').body.value as { field: { case: string } }).field.case).toBe('rootNodeId')
  })

  it('encodes the tombstone variant in tombstoneFloatingWindow', () => {
    const ctx = newCtx()
    const op = tombstoneFloatingWindow(ctx, 'win-1')
    expect(op.body.case).toBe('tombstoneFloatingWindow')
    expect((op.body.value as { windowId: string }).windowId).toBe('win-1')
  })
})

describe('liveTabsOnTile', () => {
  it('returns tabs in user-visible (position ascending, tab_id tiebreak) order, not state.tabs insertion order', async () => {
    const { create } = await import('@bufbuild/protobuf')
    const { UserCrdtStateSchema, TabRecordSchema, LWWStringSchema } = await import('~/generated/proto/leapmux/v1/user_crdt_pb')
    const { liveTabsOnTile } = await import('./ops')

    // Build a state where three live tabs share a tile but their
    // insertion order into state.tabs is REVERSED relative to their
    // LexoRank position. Without the sort, liveTabsOnTile returns them
    // in insertion order; with it, position order wins.
    const stamp = (value: string) => create(LWWStringSchema, {
      value,
      hlc: { physical: 0n, logical: 0n, clientId: '' },
    })
    const tabA = create(TabRecordSchema, {
      tabType: 1,
      tabId: 'tab-A',
      tileId: stamp('tile-1'),
      position: stamp('aaa'),
    })
    const tabB = create(TabRecordSchema, {
      tabType: 1,
      tabId: 'tab-B',
      tileId: stamp('tile-1'),
      position: stamp('mmm'),
    })
    const tabC = create(TabRecordSchema, {
      tabType: 1,
      tabId: 'tab-C',
      tileId: stamp('tile-1'),
      position: stamp('zzz'),
    })
    const state = create(UserCrdtStateSchema, {
      // Intentionally insert in C, A, B order — Object.values iteration
      // would yield that order; the sort must override it.
      tabs: { 'tab-C': tabC, 'tab-A': tabA, 'tab-B': tabB },
    })

    const got = liveTabsOnTile(state, 'tile-1')
    expect(got.map(t => t.tabId)).toEqual(['tab-A', 'tab-B', 'tab-C'])
  })
})

describe('newBatch', () => {
  it('mints a fresh batch_id and carries the ops verbatim', () => {
    const ctx = newCtx()
    const a = setNodeKind(ctx, 'n1', 1)
    const b = setNodeKind(ctx, 'n2', 1)
    const batch = newBatch([a, b])
    expect(batch.batchId).toHaveLength(48)
    expect(batch.ops).toEqual([a, b])
  })

  it('mints distinct batch_ids across calls', () => {
    const a = newBatch([])
    const b = newBatch([])
    expect(a.batchId).not.toBe(b.batchId)
  })
})

// opTarget is the SINGLE op -> entity mapping on the client (mirroring the hub's
// crdt.OpTarget). It had been copied into the checkpoint dirty-set and the
// tombstone pin, which then disagreed about what an unrecognized body means --
// the pin's fail-OPEN reading is what would re-arm the ghost-record bug for any
// op kind added later. These pin the three-valued contract that makes the two
// callers degrade in their own safe directions.
describe('opTarget', () => {
  const ctx = {
    originClientId: 'c1',
    clock: new HLCClock('c1'),
  }

  it('names the entity for every op kind that installs one', () => {
    expect(opTarget(setNodePosition(ctx, 'n1', 'p'))).toEqual({ case: 'entity', kind: 'node', id: 'n1' })
    expect(opTarget(tombstoneNode(ctx, 'n1'))).toEqual({ case: 'entity', kind: 'node', id: 'n1' })
    expect(opTarget(setTabTileId(ctx, TabType.AGENT, 't1', 'tile'))).toEqual({ case: 'entity', kind: 'tab', id: 't1' })
    expect(opTarget(tombstoneTab(ctx, TabType.AGENT, 't1'))).toEqual({ case: 'entity', kind: 'tab', id: 't1' })
    expect(opTarget(setFloatingX(ctx, 'w1', 1))).toEqual({ case: 'entity', kind: 'fw', id: 'w1' })
    expect(opTarget(tombstoneFloatingWindow(ctx, 'w1'))).toEqual({ case: 'entity', kind: 'fw', id: 'w1' })
  })

  it('identifies the tab of a revive and the workspace of a create or a tombstone', () => {
    expect(opTarget(reviveTab(ctx, TabType.AGENT, 't1'))).toEqual({ case: 'entity', kind: 'tab', id: 't1' })
    expect(opTarget(create(CrdtOpSchema, {
      body: {
        case: 'createWorkspace',
        value: { workspaceId: 'ws1', rootNodeId: 'root-1' },
      },
    }))).toEqual({ case: 'entity', kind: 'ws', id: 'ws1' })
    expect(opTarget(create(CrdtOpSchema, {
      body: {
        case: 'tombstoneWorkspace',
        value: { workspaceId: 'ws1' },
      },
    }))).toEqual({ case: 'entity', kind: 'ws', id: 'ws1' })
  })

  // A replayed op can carry no tab identity. It then targets the empty tab id,
  // which apply refuses to create, rather than throwing on the absent field.
  it('reports the empty tab id for a tab op with no tab identity', () => {
    const bodies: CrdtOp['body'][] = [
      create(CrdtOpSchema, {
        body: {
          case: 'setTabRegister',
          value: {
            field: { case: 'tileId', value: 'tile' },
          },
        },
      }).body,
      create(CrdtOpSchema, {
        body: { case: 'tombstoneTab', value: {} },
      }).body,
      create(CrdtOpSchema, {
        body: { case: 'reviveTab', value: {} },
      }).body,
    ]
    for (const body of bodies)
      expect(opTarget(create(CrdtOpSchema, { body })), body.case).toEqual({ case: 'entity', kind: 'tab', id: '' })
  })

  it('reports an empty body as provably targeting nothing', () => {
    expect(opTarget({
      body: { case: undefined },
    } as never)).toEqual({ case: 'none' })
  })

  it('reports an unrecognized body as unknown, not as nothing', () => {
    // The distinction the two consumers turn on: `none` means "safe to ignore",
    // `unknown` means "this build cannot enumerate what changed".
    expect(opTarget({
      body: { case: 'someOpFromANewerBuild', value: {} },
    } as never))
      .toEqual({ case: 'unknown' })
  })

  it('spells an entity target as the shared kind:id key, and the others as null', () => {
    expect(opTargetKey({ case: 'entity', kind: 'tab', id: 't1' })).toBe('tab:t1')
    expect(opTargetKey({ case: 'none' })).toBeNull()
    expect(opTargetKey({ case: 'unknown' })).toBeNull()
  })
})

// opHasIdentity mirrors the hub's EntityRef.HasIdentity over crdt.OpTarget. It is
// the one rule that decides whether an op identifies an entity: the hub's
// validator refuses an op that fails it, and applyOp ignores one.
describe('opHasIdentity', () => {
  const ctx = newCtx('c1')

  it('accepts an op of every kind that identifies its entity', () => {
    expect(opHasIdentity(setNodePosition(ctx, 'n1', 'p'))).toBe(true)
    expect(opHasIdentity(tombstoneNode(ctx, 'n1'))).toBe(true)
    expect(opHasIdentity(setTabTileId(ctx, TabType.AGENT, 't1', 'tile'))).toBe(true)
    expect(opHasIdentity(tombstoneTab(ctx, TabType.AGENT, 't1'))).toBe(true)
    expect(opHasIdentity(reviveTab(ctx, TabType.AGENT, 't1'))).toBe(true)
    expect(opHasIdentity(setFloatingX(ctx, 'w1', 1))).toBe(true)
    expect(opHasIdentity(tombstoneFloatingWindow(ctx, 'w1'))).toBe(true)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: {
        case: 'createWorkspace',
        value: { workspaceId: 'ws1', rootNodeId: 'r' },
      },
    }))).toBe(true)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: {
        case: 'tombstoneWorkspace',
        value: { workspaceId: 'ws1' },
      },
    }))).toBe(true)
  })

  it('refuses a node, window or workspace op with an empty id', () => {
    expect(opHasIdentity(setNodePosition(ctx, '', 'p'))).toBe(false)
    expect(opHasIdentity(tombstoneNode(ctx, ''))).toBe(false)
    expect(opHasIdentity(setFloatingX(ctx, '', 1))).toBe(false)
    expect(opHasIdentity(tombstoneFloatingWindow(ctx, ''))).toBe(false)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: {
        case: 'createWorkspace',
        value: { workspaceId: '', rootNodeId: 'r' },
      },
    }))).toBe(false)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: {
        case: 'tombstoneWorkspace',
        value: { workspaceId: '' },
      },
    }))).toBe(false)
  })

  // A tab's identity is its type AND its id, so a tab op that lacks either, or
  // carries no TabIdent at all, identifies no tab.
  it('refuses a tab op with no tab id, no tab type or no tab', () => {
    expect(opHasIdentity(setTabTileId(ctx, TabType.AGENT, '', 'tile'))).toBe(false)
    expect(opHasIdentity(tombstoneTab(ctx, TabType.UNSPECIFIED, 't1'))).toBe(false)
    expect(opHasIdentity(reviveTab(ctx, TabType.UNSPECIFIED, 't1'))).toBe(false)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: {
        case: 'setTabRegister',
        value: {
          field: { case: 'tileId', value: 'tile' },
        },
      },
    }))).toBe(false)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: { case: 'tombstoneTab', value: {} },
    }))).toBe(false)
    expect(opHasIdentity(create(CrdtOpSchema, {
      body: { case: 'reviveTab', value: {} },
    }))).toBe(false)
  })

  it('refuses an empty body and a body this build does not know', () => {
    expect(opHasIdentity(create(CrdtOpSchema, {}))).toBe(false)
    expect(opHasIdentity({
      body: { case: 'someOpFromANewerBuild', value: {} },
    } as never)).toBe(false)
  })
})

// selectsNoRegister mirrors the hub's selectsNoRegister: a register op whose
// field oneof selects nothing writes nothing.
describe('selectsNoRegister', () => {
  const ctx = newCtx('c1')

  it('reports a register op of every family that selects no field', () => {
    expect(selectsNoRegister(create(CrdtOpSchema, {
      body: {
        case: 'setNodeRegister',
        value: { nodeId: 'n1' },
      },
    }))).toBe(true)
    expect(selectsNoRegister(create(CrdtOpSchema, {
      body: {
        case: 'setTabRegister',
        value: {
          tab: { tabType: TabType.AGENT, tabId: 't1' },
        },
      },
    }))).toBe(true)
    expect(selectsNoRegister(create(CrdtOpSchema, {
      body: {
        case: 'setFloatingWindowRegister',
        value: { windowId: 'w1' },
      },
    }))).toBe(true)
  })

  it('passes a register op that selects a field', () => {
    expect(selectsNoRegister(setNodePosition(ctx, 'n1', 'p'))).toBe(false)
    expect(selectsNoRegister(setTabTileId(ctx, TabType.AGENT, 't1', 'tile'))).toBe(false)
    expect(selectsNoRegister(setFloatingX(ctx, 'w1', 1))).toBe(false)
  })

  it('passes every op that is not a register op', () => {
    expect(selectsNoRegister(tombstoneNode(ctx, 'n1'))).toBe(false)
    expect(selectsNoRegister(tombstoneTab(ctx, TabType.AGENT, 't1'))).toBe(false)
    expect(selectsNoRegister(reviveTab(ctx, TabType.AGENT, 't1'))).toBe(false)
    expect(selectsNoRegister(tombstoneFloatingWindow(ctx, 'w1'))).toBe(false)
    expect(selectsNoRegister(create(CrdtOpSchema, {
      body: {
        case: 'createWorkspace',
        value: { workspaceId: 'ws1', rootNodeId: 'r' },
      },
    }))).toBe(false)
    expect(selectsNoRegister(create(CrdtOpSchema, {
      body: {
        case: 'tombstoneWorkspace',
        value: { workspaceId: 'ws1' },
      },
    }))).toBe(false)
    expect(selectsNoRegister(create(CrdtOpSchema, {}))).toBe(false)
  })
})
