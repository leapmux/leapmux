import type { CrdtOp } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { create, fromBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { HLCSchema, NodeKind, WorkspaceContentsRecordSchema } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import {
  CrdtOpSchema,
  CreateWorkspaceOpSchema,
  ReviveTabOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneNodeOpSchema,
  TombstoneTabOpSchema,
  TombstoneWorkspaceOpSchema,
} from '~/generated/proto/leapmux/v1/user_ops_pb'
import { applyOp, newState } from './apply'

function hlc(physical: bigint, logical: bigint, clientId: string) {
  return create(HLCSchema, { physical, logical, clientId })
}

function setNodeKindOp(nodeId: string, kind: NodeKind, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, {
        nodeId,
        field: { case: 'kind', value: kind },
      }),
    },
  })
}

function setNodePositionOp(nodeId: string, position: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, {
        nodeId,
        field: { case: 'position', value: position },
      }),
    },
  })
}

function setNodeParentOp(nodeId: string, parentId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, {
        nodeId,
        field: { case: 'parentId', value: parentId },
      }),
    },
  })
}

function tombstoneNodeOp(nodeId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'tombstoneNode',
      value: create(TombstoneNodeOpSchema, { nodeId }),
    },
  })
}

function setTabTileIdOp(tabId: string, tileId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setTabRegister',
      value: create(SetTabRegisterOpSchema, {
        tab: { tabType: TabType.AGENT, tabId },
        field: { case: 'tileId', value: tileId },
      }),
    },
  })
}

function tombstoneTabOp(tabId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'tombstoneTab',
      value: create(TombstoneTabOpSchema, {
        tab: { tabType: TabType.AGENT, tabId },
      }),
    },
  })
}

function reviveTabOp(tabId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'reviveTab',
      value: create(ReviveTabOpSchema, {
        tab: { tabType: TabType.AGENT, tabId },
      }),
    },
  })
}

function setFloatingXOp(windowId: string, x: number, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: {
      case: 'setFloatingWindowRegister',
      value: create(SetFloatingWindowRegisterOpSchema, {
        windowId,
        field: { case: 'x', value: x },
      }),
    },
  })
}

describe('applyOp', () => {
  it('writes a fresh register and is idempotent on re-application', () => {
    const state = newState('user')
    const op = setNodeKindOp('n1', NodeKind.LEAF, 10n, 0n, 'a')
    applyOp(state, op)
    expect(state.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
    applyOp(state, op)
    expect(state.nodes.n1?.kind?.value).toBe(NodeKind.LEAF)
  })

  it('higher HLC wins LWW', () => {
    const state = newState('user')
    applyOp(state, setNodePositionOp('n1', 'A', 10n, 0n, 'a'))
    applyOp(state, setNodePositionOp('n1', 'B', 20n, 0n, 'b'))
    expect(state.nodes.n1?.position?.value).toBe('B')
  })

  it('lower HLC drops on existing register', () => {
    const state = newState('user')
    applyOp(state, setNodePositionOp('n1', 'B', 20n, 0n, 'b'))
    applyOp(state, setNodePositionOp('n1', 'A', 10n, 0n, 'a'))
    expect(state.nodes.n1?.position?.value).toBe('B')
  })

  it('parent_id is set-once at the apply layer', () => {
    const state = newState('user')
    applyOp(state, setNodeParentOp('n1', 'P1', 10n, 0n, 'a'))
    applyOp(state, setNodeParentOp('n1', 'P2', 20n, 0n, 'b'))
    expect(state.nodes.n1?.parentId).toBe('P1')
  })

  it('tombstone clears registers and drops later sets', () => {
    const state = newState('user')
    applyOp(state, setNodePositionOp('n1', 'A', 10n, 0n, 'a'))
    applyOp(state, tombstoneNodeOp('n1', 20n, 0n, 'a'))
    expect(state.nodes.n1?.position).toBeUndefined()
    applyOp(state, setNodePositionOp('n1', 'C', 30n, 0n, 'a'))
    expect(state.nodes.n1?.position).toBeUndefined()
  })

  it('a Set with HLC older than the existing tombstone drops too', () => {
    const state = newState('user')
    applyOp(state, tombstoneNodeOp('n1', 30n, 0n, 'a'))
    applyOp(state, setNodePositionOp('n1', 'X', 20n, 0n, 'a'))
    expect(state.nodes.n1?.position).toBeUndefined()
  })

  it('tab tile_id LWW', () => {
    const state = newState('user')
    applyOp(state, setTabTileIdOp('t1', 'A', 10n, 0n, 'a'))
    applyOp(state, setTabTileIdOp('t1', 'B', 20n, 0n, 'b'))
    expect(state.tabs.t1?.tileId?.value).toBe('B')
  })

  it('tab tombstone clears registers', () => {
    const state = newState('user')
    applyOp(state, setTabTileIdOp('t1', 'A', 10n, 0n, 'a'))
    applyOp(state, tombstoneTabOp('t1', 20n, 0n, 'a'))
    expect(state.tabs.t1?.tileId).toBeUndefined()
  })

  it('revive clears a newer tombstone and lets later sets land', () => {
    const state = newState('user')
    applyOp(state, setTabTileIdOp('t1', 'A', 10n, 0n, 'a'))
    applyOp(state, tombstoneTabOp('t1', 20n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeDefined()
    // Revive at a newer HLC clears the tombstone.
    applyOp(state, reviveTabOp('t1', 30n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeUndefined()
    // A later set now lands (the tab is live again).
    applyOp(state, setTabTileIdOp('t1', 'B', 40n, 0n, 'a'))
    expect(state.tabs.t1?.tileId?.value).toBe('B')
  })

  it('revive older than tombstone is a no-op (remove-wins for concurrent)', () => {
    const state = newState('user')
    applyOp(state, tombstoneTabOp('t1', 50n, 0n, 'a'))
    applyOp(state, reviveTabOp('t1', 40n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeDefined()
  })

  it('tombstone older than a revive does not re-close a revived tab', () => {
    // LWW on the revive register: a tombstone whose HLC is between the original
    // tombstone and the revive must not re-tombstone. Without recording the
    // revive HLC, the cleared register had no "last write" and any tombstone
    // re-closed the tab.
    const state = newState('user')
    applyOp(state, tombstoneTabOp('t1', 10n, 0n, 'a'))
    applyOp(state, reviveTabOp('t1', 30n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeUndefined()
    // Tombstone at HLC 20 (older than the revive at 30): must NOT re-close.
    applyOp(state, tombstoneTabOp('t1', 20n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeUndefined()
    // A tombstone strictly newer than the revive still wins (genuine close).
    applyOp(state, tombstoneTabOp('t1', 40n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeDefined()
  })

  it('a redelivered older revive does not regress revivedAt', () => {
    // revivedAt is a monotone max: a redelivered/out-of-order revive older
    // than the last successful revive must not regress it. Otherwise a later
    // tombstone (newer than the stale revive, older than the real one) would
    // pass both LWW gates and re-close a tab the user reopened.
    const state = newState('user')
    applyOp(state, tombstoneTabOp('t1', 10n, 0n, 'a'))
    applyOp(state, reviveTabOp('t1', 30n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeUndefined()
    const revivedAt30 = state.tabs.t1?.revivedAt
    expect(revivedAt30).toBeDefined()
    // Redelivered revive at 25 (older than 30): must not change revivedAt.
    applyOp(state, reviveTabOp('t1', 25n, 0n, 'a'))
    expect(state.tabs.t1?.revivedAt).toBe(revivedAt30)
    // A tombstone at 27 (newer than the stale 25, older than the real 30) must
    // not re-close the tab.
    applyOp(state, tombstoneTabOp('t1', 27n, 0n, 'a'))
    expect(state.tabs.t1?.tombstoneAt).toBeUndefined()
  })

  it('revive of an unseen tab materializes a live record', () => {
    const state = newState('user')
    applyOp(state, reviveTabOp('t-new', 10n, 0n, 'a'))
    expect(state.tabs['t-new']).toBeDefined()
    expect(state.tabs['t-new']?.tombstoneAt).toBeUndefined()
  })

  it('-0.0 normalizes to +0.0 on double registers', () => {
    const state = newState('user')
    applyOp(state, setFloatingXOp('w1', -0, 10n, 0n, 'a'))
    const x = state.floatingWindows.w1?.x?.value
    // Object.is distinguishes -0 from +0; the apply layer must
    // canonicalize so the bit pattern is +0.
    expect(Object.is(x, -0)).toBe(false)
    expect(x).toBe(0)
  })

  // Regression pin: a create op arriving on a subscriber whose
  // `UserMaterialized` predated the workspace finds `state.workspaces[wsID]`
  // absent. The apply layer must create the record; without it
  // `state.workspaces[wsID].rootNodeId` stayed empty, `seedTabIntoNewWorkspace`
  // timed out, and the new workspace rendered an empty tile via the layout
  // store's FALLBACK_LEAF instead of the real root.
  it('createWorkspace creates the workspace record with its root when absent', () => {
    const state = newState('user')
    expect(state.workspaces.w1).toBeUndefined()
    const op = create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1', rootNodeId: 'root-w1' }),
      },
    })
    applyOp(state, op)
    expect(state.workspaces.w1).toBeDefined()
    expect(state.workspaces.w1?.workspaceId).toBe('w1')
    expect(state.workspaces.w1?.rootNodeId).toBe('root-w1')
  })

  it('createWorkspace preserves an already-set rootNodeId (set-once)', () => {
    const state = newState('user')
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1', rootNodeId: 'first-root' }),
      },
    }))
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(20n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1', rootNodeId: 'second-root' }),
      },
    }))
    // Set-once semantics: the second register must not overwrite.
    expect(state.workspaces.w1?.rootNodeId).toBe('first-root')
  })

  // createWorkspace is the lifecycle create op that carries workspace-map
  // membership through the serialized submit pipeline (replacing the old
  // out-of-band MutateInternal). A create with no root still creates the
  // record. Mirrors backend `applyCreateWorkspace`.
  it('createWorkspace with no root creates an empty workspace record', () => {
    const state = newState('user')
    expect(state.workspaces.w1).toBeUndefined()
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1' }),
      },
    }))
    expect(state.workspaces.w1).toBeDefined()
    expect(state.workspaces.w1?.workspaceId).toBe('w1')
    expect(state.workspaces.w1?.rootNodeId).toBe('')
  })

  it('createWorkspace with no root does not clear a rooted record', () => {
    // A re-drained create seed batch (after a transient consume fault) must
    // not clear an existing root_node_id the user has since populated.
    const state = newState('user')
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1', rootNodeId: 'root-w1' }),
      },
    }))
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(20n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1' }),
      },
    }))
    expect(state.workspaces.w1?.rootNodeId).toBe('root-w1')
  })

  // tombstoneWorkspace removes the WorkspaceContentsRecord map entry — the
  // lifecycle delete op. Without an apply case the client would keep a stale
  // record after a delete until reconnect; this pins that it removes it.
  it('tombstoneWorkspace removes the workspace record', () => {
    const state = newState('user')
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1' }),
      },
    }))
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(20n, 0n, 'hub'),
      body: {
        case: 'tombstoneWorkspace',
        value: create(TombstoneWorkspaceOpSchema, { workspaceId: 'w1' }),
      },
    }))
    expect(state.workspaces.w1).toBeUndefined()
  })

  it('tombstoneWorkspace is idempotent on an already-absent workspace', () => {
    // Re-draining a delete batch (after a consume fault) must be a no-op,
    // not a throw on a missing key.
    const state = newState('user')
    expect(() => applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'tombstoneWorkspace',
        value: create(TombstoneWorkspaceOpSchema, { workspaceId: 'w1' }),
      },
    }))).not.toThrow()
    expect(state.workspaces.w1).toBeUndefined()
  })
})

describe('applyOp with an untyped tab', () => {
  // A tab identity is a type and an id. protobuf-es decodes an op stored under
  // the older numbering as a tab id with no type; applying it must neither
  // create a tab of no type nor close a tab it cannot type.
  it('ignores a tab op with no tab type', () => {
    const state = newState('user')
    applyOp(state, setTabTileIdOp('t1', 'root', 5n, 0n, 'a'))
    const untyped = (body: CrdtOp['body']) => create(CrdtOpSchema, {
      canonicalHlc: hlc(20n, 0n, 'a'),
      body,
    })

    applyOp(state, untyped({
      case: 'setTabRegister',
      value: create(SetTabRegisterOpSchema, {
        tab: { tabId: 'fresh' },
        field: { case: 'tileId', value: 'x' },
      }),
    }))
    applyOp(state, untyped({
      case: 'tombstoneTab',
      value: create(TombstoneTabOpSchema, {
        tab: { tabId: 't1' },
      }),
    }))
    applyOp(state, untyped({
      case: 'reviveTab',
      value: create(ReviveTabOpSchema, {
        tab: { tabId: 'fresh' },
      }),
    }))

    expect(state.tabs.fresh).toBeUndefined()
    expect(state.tabs.t1?.tombstoneAt).toBeUndefined()
    expect(state.tabs.t1?.tileId?.value).toBe('root')
  })
})

describe('applyOp with an empty tab id', () => {
  // A replayed or misdecoded op can carry no tab id. Applying it must not
  // create a record under the empty key, which every reader would then treat as
  // a real tab.
  it('ignores a tab op with no tab id', () => {
    const state = newState('user')

    applyOp(state, setTabTileIdOp('', 'tile', 10n, 0n, 'a'))
    applyOp(state, tombstoneTabOp('', 11n, 0n, 'a'))

    expect(state.tabs['']).toBeUndefined()
    expect(Object.keys(state.tabs)).toHaveLength(0)
  })
})

// A register op that selects no register writes nothing, so it must not create
// a record either. The hub refuses such an op from a client, but a replayed
// journal op or a stored checkpoint skips that check.
describe('applyOp register ops with no field', () => {
  it('creates no record for a register op with no field', () => {
    const state = newState('user')
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: {
        case: 'setNodeRegister',
        value: create(SetNodeRegisterOpSchema, { nodeId: 'n1' }),
      },
    }))
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(11n, 0n, 'a'),
      body: {
        case: 'setTabRegister',
        value: create(SetTabRegisterOpSchema, {
          tab: { tabType: TabType.AGENT, tabId: 't1' },
        }),
      },
    }))
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(12n, 0n, 'a'),
      body: {
        case: 'setFloatingWindowRegister',
        value: create(SetFloatingWindowRegisterOpSchema, { windowId: 'fw1' }),
      },
    }))

    expect(Object.keys(state.nodes)).toHaveLength(0)
    expect(Object.keys(state.tabs)).toHaveLength(0)
    expect(Object.keys(state.floatingWindows)).toHaveLength(0)
  })

  // workspace_id moved from field 9 to field 16, so the payload oneof runs
  // from 10 with no hole and fields 10-15 keep their meaning. A stored op that
  // wrote field 9 now decodes with no field selected.
  it('ignores a stored floating window op of the old numbering', () => {
    // window_id = "fw1" (field 1), then the old workspace_id = "w1" (field 9).
    const stored = new Uint8Array([
      0x0A,
      0x03,
      0x66,
      0x77,
      0x31,
      0x4A,
      0x02,
      0x77,
      0x31,
    ])
    const op = fromBinary(SetFloatingWindowRegisterOpSchema, stored)
    expect(op.windowId).toBe('fw1')
    expect(op.field.case).toBeUndefined()

    const state = newState('user')
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: { case: 'setFloatingWindowRegister', value: op },
    }))
    expect(Object.keys(state.floatingWindows)).toHaveLength(0)
  })
})

describe('applyOp createWorkspace edge cases', () => {
  it('sets the root of a record that has none', () => {
    const state = newState('user')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1' })
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: 'w1', rootNodeId: 'root-w1' }),
      },
    }))
    expect(state.workspaces.w1?.rootNodeId).toBe('root-w1')
  })

  it('ignores an empty workspace id', () => {
    const state = newState('user')
    applyOp(state, create(CrdtOpSchema, {
      canonicalHlc: hlc(10n, 0n, 'hub'),
      body: {
        case: 'createWorkspace',
        value: create(CreateWorkspaceOpSchema, { workspaceId: '', rootNodeId: 'root' }),
      },
    }))
    expect(Object.keys(state.workspaces)).toHaveLength(0)
  })
})
