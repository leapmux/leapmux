import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { HLCSchema, NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import {
  OrgOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  SetWorkspaceRootNodeOpSchema,
  TombstoneNodeOpSchema,
  TombstoneTabOpSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'

function hlc(physical: bigint, logical: bigint, clientId: string) {
  return create(HLCSchema, { physical, logical, clientId })
}

function setNodeKindOp(nodeId: string, kind: NodeKind, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'kind', value: kind } }),
    },
  })
}

function setNodePositionOp(nodeId: string, position: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'position', value: position } }),
    },
  })
}

function setNodeParentOp(nodeId: string, parentId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'parentId', value: parentId } }),
    },
  })
}

function tombstoneNodeOp(nodeId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneNode', value: create(TombstoneNodeOpSchema, { nodeId }) },
  })
}

function setTabTileIdOp(tabId: string, tileId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setTabRegister',
      value: create(SetTabRegisterOpSchema, { tabType: TabType.AGENT, tabId, field: { case: 'tileId', value: tileId } }),
    },
  })
}

function tombstoneTabOp(tabId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneTab', value: create(TombstoneTabOpSchema, { tabType: TabType.AGENT, tabId }) },
  })
}

function setFloatingXOp(windowId: string, x: number, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setFloatingWindowRegister',
      value: create(SetFloatingWindowRegisterOpSchema, { windowId, field: { case: 'x', value: x } }),
    },
  })
}

describe('applyOp', () => {
  it('writes a fresh register and is idempotent on re-application', () => {
    const state = newState('org')
    const op = setNodeKindOp('n1', NodeKind.LEAF, 10n, 0n, 'a')
    applyOp(state, op)
    expect(state.nodes.n1.kind?.value).toBe(NodeKind.LEAF)
    applyOp(state, op)
    expect(state.nodes.n1.kind?.value).toBe(NodeKind.LEAF)
  })

  it('higher HLC wins LWW', () => {
    const state = newState('org')
    applyOp(state, setNodePositionOp('n1', 'A', 10n, 0n, 'a'))
    applyOp(state, setNodePositionOp('n1', 'B', 20n, 0n, 'b'))
    expect(state.nodes.n1.position?.value).toBe('B')
  })

  it('lower HLC drops on existing register', () => {
    const state = newState('org')
    applyOp(state, setNodePositionOp('n1', 'B', 20n, 0n, 'b'))
    applyOp(state, setNodePositionOp('n1', 'A', 10n, 0n, 'a'))
    expect(state.nodes.n1.position?.value).toBe('B')
  })

  it('parent_id is set-once at the apply layer', () => {
    const state = newState('org')
    applyOp(state, setNodeParentOp('n1', 'P1', 10n, 0n, 'a'))
    applyOp(state, setNodeParentOp('n1', 'P2', 20n, 0n, 'b'))
    expect(state.nodes.n1.parentId).toBe('P1')
  })

  it('tombstone clears registers and drops later sets', () => {
    const state = newState('org')
    applyOp(state, setNodePositionOp('n1', 'A', 10n, 0n, 'a'))
    applyOp(state, tombstoneNodeOp('n1', 20n, 0n, 'a'))
    expect(state.nodes.n1.position).toBeUndefined()
    applyOp(state, setNodePositionOp('n1', 'C', 30n, 0n, 'a'))
    expect(state.nodes.n1.position).toBeUndefined()
  })

  it('a Set with HLC older than the existing tombstone drops too', () => {
    const state = newState('org')
    applyOp(state, tombstoneNodeOp('n1', 30n, 0n, 'a'))
    applyOp(state, setNodePositionOp('n1', 'X', 20n, 0n, 'a'))
    expect(state.nodes.n1.position).toBeUndefined()
  })

  it('tab tile_id LWW', () => {
    const state = newState('org')
    applyOp(state, setTabTileIdOp('t1', 'A', 10n, 0n, 'a'))
    applyOp(state, setTabTileIdOp('t1', 'B', 20n, 0n, 'b'))
    expect(state.tabs.t1.tileId?.value).toBe('B')
  })

  it('tab tombstone clears registers', () => {
    const state = newState('org')
    applyOp(state, setTabTileIdOp('t1', 'A', 10n, 0n, 'a'))
    applyOp(state, tombstoneTabOp('t1', 20n, 0n, 'a'))
    expect(state.tabs.t1.tileId).toBeUndefined()
  })

  it('-0.0 normalizes to +0.0 on double registers', () => {
    const state = newState('org')
    applyOp(state, setFloatingXOp('w1', -0, 10n, 0n, 'a'))
    const x = state.floatingWindows.w1.x?.value
    // Object.is distinguishes -0 from +0; the apply layer must
    // canonicalize so the bit pattern is +0.
    expect(Object.is(x, -0)).toBe(false)
    expect(x).toBe(0)
  })

  // Regression pin: when the seed-batch `SetWorkspaceRootNode` op
  // arrives on a subscriber whose `OrgMaterialized` predated the
  // workspace, `state.workspaces[wsID]` is absent — the hub seeds
  // the record via `MutateInternal` which is NOT itself broadcast.
  // The apply layer must lazy-create the record; without this the
  // op was a silent no-op, `state.workspaces[wsID].rootNodeId` stayed
  // empty, `seedTabIntoNewWorkspace` timed out, and the new
  // workspace rendered an empty tile via the layout store's
  // FALLBACK_LEAF instead of the real root.
  it('setWorkspaceRootNode lazy-creates the workspace record when absent', () => {
    const state = newState('org')
    expect(state.workspaces.w1).toBeUndefined()
    const op = create(OrgOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'setWorkspaceRootNode',
        value: create(SetWorkspaceRootNodeOpSchema, { workspaceId: 'w1', rootNodeId: 'root-w1' }),
      },
    })
    applyOp(state, op)
    expect(state.workspaces.w1).toBeDefined()
    expect(state.workspaces.w1.workspaceId).toBe('w1')
    expect(state.workspaces.w1.rootNodeId).toBe('root-w1')
  })

  it('setWorkspaceRootNode preserves an already-set rootNodeId (set-once)', () => {
    const state = newState('org')
    applyOp(state, create(OrgOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'setWorkspaceRootNode',
        value: create(SetWorkspaceRootNodeOpSchema, { workspaceId: 'w1', rootNodeId: 'first-root' }),
      },
    }))
    applyOp(state, create(OrgOpSchema, {
      canonicalHlc: hlc(20n, 0n, 'hub'),
      body: {
        case: 'setWorkspaceRootNode',
        value: create(SetWorkspaceRootNodeOpSchema, { workspaceId: 'w1', rootNodeId: 'second-root' }),
      },
    }))
    // Set-once semantics: the second register must not overwrite.
    expect(state.workspaces.w1.rootNodeId).toBe('first-root')
  })
})
