import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { HLCSchema, NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import { OrgOpSchema, SetNodeRegisterOpSchema, SetTabRegisterOpSchema, SetWorkspaceRootNodeOpSchema, TombstoneNodeOpSchema } from '~/generated/leapmux/v1/org_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, HLCClock, hlcCmp, hlcIsZero, newState, project, projectWorkspace } from '~/lib/crdt'

function hlc(physical: bigint, logical: bigint, clientId: string) {
  return create(HLCSchema, { physical, logical, clientId })
}

function setNodeKind(nodeId: string, kind: NodeKind, atHlc: ReturnType<typeof hlc>) {
  return create(OrgOpSchema, {
    opId: `set-${nodeId}-kind`,
    canonicalHlc: atHlc,
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, {
        nodeId,
        field: { case: 'kind', value: kind },
      }),
    },
  })
}

function setNodePosition(nodeId: string, position: string, atHlc: ReturnType<typeof hlc>) {
  return create(OrgOpSchema, {
    opId: `set-${nodeId}-pos-${position}`,
    canonicalHlc: atHlc,
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, {
        nodeId,
        field: { case: 'position', value: position },
      }),
    },
  })
}

function setNodeParentId(nodeId: string, parentId: string, atHlc: ReturnType<typeof hlc>) {
  return create(OrgOpSchema, {
    opId: `set-${nodeId}-parent`,
    canonicalHlc: atHlc,
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, {
        nodeId,
        field: { case: 'parentId', value: parentId },
      }),
    },
  })
}

function tombstoneNode(nodeId: string, atHlc: ReturnType<typeof hlc>) {
  return create(OrgOpSchema, {
    opId: `tomb-${nodeId}`,
    canonicalHlc: atHlc,
    body: {
      case: 'tombstoneNode',
      value: create(TombstoneNodeOpSchema, { nodeId }),
    },
  })
}

function setTabTileId(tabId: string, tileId: string, atHlc: ReturnType<typeof hlc>) {
  return create(OrgOpSchema, {
    opId: `set-${tabId}-tile`,
    canonicalHlc: atHlc,
    body: {
      case: 'setTabRegister',
      value: create(SetTabRegisterOpSchema, {
        tabType: TabType.AGENT,
        tabId,
        field: { case: 'tileId', value: tileId },
      }),
    },
  })
}

function setWorkspaceRootNode(workspaceId: string, rootNodeId: string, atHlc: ReturnType<typeof hlc>) {
  return create(OrgOpSchema, {
    opId: `set-ws-root-${workspaceId}`,
    canonicalHlc: atHlc,
    body: {
      case: 'setWorkspaceRootNode',
      value: create(SetWorkspaceRootNodeOpSchema, { workspaceId, rootNodeId }),
    },
  })
}

describe('crdt apply', () => {
  it('lww: higher hlc wins', () => {
    const state = newState('org')
    applyOp(state, setNodePosition('n1', 'A', hlc(10n, 0n, 'a')))
    applyOp(state, setNodePosition('n1', 'B', hlc(20n, 0n, 'b')))
    expect(state.nodes.n1.position?.value).toBe('B')
  })

  it('lww: lower hlc drops', () => {
    const state = newState('org')
    applyOp(state, setNodePosition('n1', 'B', hlc(20n, 0n, 'b')))
    applyOp(state, setNodePosition('n1', 'A', hlc(10n, 0n, 'a')))
    expect(state.nodes.n1.position?.value).toBe('B')
  })

  it('tombstone clears non-tombstone registers', () => {
    const state = newState('org')
    applyOp(state, setNodePosition('n1', 'A', hlc(10n, 0n, 'a')))
    applyOp(state, tombstoneNode('n1', hlc(20n, 0n, 'a')))
    expect(state.nodes.n1.position).toBeUndefined()
    expect(hlcIsZero(state.nodes.n1.tombstoneAt)).toBe(false)
  })

  it('set after tombstone (later HLC) drops', () => {
    const state = newState('org')
    applyOp(state, tombstoneNode('n1', hlc(20n, 0n, 'a')))
    applyOp(state, setNodePosition('n1', 'X', hlc(30n, 0n, 'a')))
    expect(state.nodes.n1.position).toBeUndefined()
  })

  it('parent_id is set-once', () => {
    const state = newState('org')
    applyOp(state, setNodeParentId('n1', 'P1', hlc(10n, 0n, 'a')))
    applyOp(state, setNodeParentId('n1', 'P2', hlc(20n, 0n, 'b')))
    expect(state.nodes.n1.parentId).toBe('P1')
  })

  // Regression: pre-fix, `applySetWorkspaceRootNode` early-returned
  // when `state.workspaces[workspaceId]` was missing. The hub seeds
  // an empty `WorkspaceContentsRecord` via MutateInternal before
  // broadcasting the seed batch — but that internal mutation is not
  // itself part of the broadcast. For any subscriber whose initial
  // `OrgMaterialized` predated the workspace, the
  // `SetWorkspaceRootNode` op is the FIRST signal that the workspace
  // exists. The old early-return left `state.workspaces[wsId]`
  // undefined, the agent tab seed waited forever for `rootNodeId !=
  // ''`, and the new workspace appeared tile-less.
  it('setWorkspaceRootNode lazy-creates the WorkspaceContentsRecord', () => {
    const state = newState('org')
    expect(state.workspaces.w1).toBeUndefined()
    applyOp(state, setWorkspaceRootNode('w1', 'root1', hlc(1n, 0n, 'a')))
    expect(state.workspaces.w1).toBeDefined()
    expect(state.workspaces.w1.rootNodeId).toBe('root1')
  })

  // The op is set-once: re-applying with a different root id must not
  // overwrite an already-seeded record.
  it('setWorkspaceRootNode is set-once on the rootNodeId slot', () => {
    const state = newState('org')
    applyOp(state, setWorkspaceRootNode('w1', 'root1', hlc(1n, 0n, 'a')))
    applyOp(state, setWorkspaceRootNode('w1', 'root2', hlc(2n, 0n, 'a')))
    expect(state.workspaces.w1.rootNodeId).toBe('root1')
  })

  it('-0.0 normalizes to +0.0 on double registers', () => {
    const state = newState('org')
    const op = create(OrgOpSchema, {
      opId: 'fw-x',
      canonicalHlc: hlc(10n, 0n, 'a'),
      body: {
        case: 'setFloatingWindowRegister',
        value: {
          $typeName: 'leapmux.v1.SetFloatingWindowRegisterOp',
          windowId: 'w1',
          field: { case: 'x', value: -0 },
        } as never,
      },
    })
    applyOp(state, op)
    expect(Object.is(state.floatingWindows.w1.x?.value, 0)).toBe(true)
    expect(Object.is(state.floatingWindows.w1.x?.value, -0)).toBe(false)
  })
})

describe('crdt hlc', () => {
  it('cmp orders lex by (physical, logical, clientId)', () => {
    const a = hlc(10n, 0n, 'a')
    const b = hlc(10n, 1n, 'a')
    const c = hlc(11n, 0n, 'a')
    const d = hlc(10n, 0n, 'b')
    expect(hlcCmp(a, b)).toBe(-1)
    expect(hlcCmp(b, c)).toBe(-1)
    expect(hlcCmp(a, d)).toBe(-1)
    expect(hlcCmp(a, hlc(10n, 0n, 'a'))).toBe(0)
  })

  it('clock tick monotonic + observe seeds past', () => {
    const c = new HLCClock('client-1')
    const t1 = c.tick(100)
    const t2 = c.tick(100)
    const t3 = c.tick(200)
    expect(t1.logical).toBe(0n)
    expect(t2.logical).toBe(1n)
    expect(t3.physical).toBe(200n)

    c.observe(hlc(500n, 7n, 'other'))
    const t4 = c.tick(100)
    expect(t4.physical).toBe(500n)
    expect(t4.logical).toBe(8n)
  })
})

describe('crdt project', () => {
  it('skips tombstoned nodes from main tree', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'root1' } as never
    applyOp(state, setNodeKind('root1', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    applyOp(state, setNodeKind('child', NodeKind.LEAF, hlc(2n, 0n, 'a')))
    applyOp(state, setNodeParentId('child', 'root1', hlc(2n, 1n, 'a')))
    applyOp(state, tombstoneNode('child', hlc(3n, 0n, 'a')))

    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    expect(ws.mainTree.children).toHaveLength(0)
  })

  it('drops tabs whose tile_id resolves to nothing', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'root1' } as never
    applyOp(state, setNodeKind('root1', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    // Tab points at a non-existent tile.
    applyOp(state, setTabTileId('t1', 'ghost', hlc(2n, 0n, 'a')))
    const proj = project(state)
    expect(proj.renderedTabs.find(t => t.tabId === 't1')).toBeUndefined()
    expect(proj.ownedTabs.find(t => t.tabId === 't1')).toBeUndefined()
  })

  it('renders single-child SPLIT as the surviving child with split nodeId preserved', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'root1' } as never
    applyOp(state, setNodeKind('root1', NodeKind.SPLIT, hlc(1n, 0n, 'a')))
    applyOp(state, setNodeKind('child', NodeKind.LEAF, hlc(2n, 0n, 'a')))
    applyOp(state, setNodeParentId('child', 'root1', hlc(2n, 1n, 'a')))
    applyOp(state, setNodePosition('child', 'N', hlc(2n, 2n, 'a')))
    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    expect(ws.mainTree.nodeId).toBe('root1')
    expect(ws.mainTree.kind).toBe(NodeKind.LEAF)
  })

  // Regression: `registeredRoots` had an inverted `!hlcIsZero` guard
  // that excluded LIVE floating windows from the root set instead of
  // tombstoned ones. As a result the projection couldn't resolve any
  // tab whose tile lived inside a floating window — `resolveTileWorkspace`
  // walked up to the floating-window root and didn't find it in the
  // root set, so the tab was dropped from `renderedTabs`/`ownedTabs`.
  // `reconcileFromProjection` then deleted the tab from the local tab
  // store, and a popped-out tab vanished immediately.
  //
  // The backend equivalent `backend/internal/hub/crdt/project.go`
  // skips tombstoned windows with `if !HLCIsZero(fw.GetTombstoneAt()) continue`;
  // the two implementations must agree on this guard.
  it('renders a tab whose tile is a LIVE floating window root', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'mainRoot' } as never
    applyOp(state, setNodeKind('mainRoot', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    // Floating window with its own LEAF root, both live.
    applyOp(state, setNodeKind('fwRoot', NodeKind.LEAF, hlc(2n, 0n, 'a')))
    state.floatingWindows.fw1 = {
      $typeName: 'leapmux.v1.FloatingWindowRecord',
      windowId: 'fw1',
      rootNodeId: 'fwRoot',
      workspaceId: { $typeName: 'leapmux.v1.StringRegister', value: 'w1' },
    } as never
    // Tab sits on the floating window's root tile.
    applyOp(state, setTabTileId('tab1', 'fwRoot', hlc(3n, 0n, 'a')))

    const proj = project(state)
    const rendered = proj.renderedTabs.find(t => t.tabId === 'tab1')
    expect(rendered).toBeDefined()
    expect(rendered!.workspaceId).toBe('w1')
    expect(rendered!.tileId).toBe('fwRoot')
    const owned = proj.ownedTabs.find(t => t.tabId === 'tab1')
    expect(owned).toBeDefined()
    expect(owned!.workspaceId).toBe('w1')
  })

  // Mirror regression: tabs whose tile lives in a TOMBSTONED floating
  // window must NOT render. (The fix mustn't simply flip the guard
  // without the !== '' check; this test covers the negative case too.)
  it('drops tabs whose tile is a TOMBSTONED floating window root', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'mainRoot' } as never
    applyOp(state, setNodeKind('mainRoot', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    applyOp(state, setNodeKind('fwRoot', NodeKind.LEAF, hlc(2n, 0n, 'a')))
    state.floatingWindows.fw1 = {
      $typeName: 'leapmux.v1.FloatingWindowRecord',
      windowId: 'fw1',
      rootNodeId: 'fwRoot',
      workspaceId: { $typeName: 'leapmux.v1.StringRegister', value: 'w1' },
      tombstoneAt: hlc(5n, 0n, 'a'),
    } as never
    applyOp(state, setTabTileId('tab1', 'fwRoot', hlc(3n, 0n, 'a')))

    const proj = project(state)
    expect(proj.renderedTabs.find(t => t.tabId === 'tab1')).toBeUndefined()
    expect(proj.ownedTabs.find(t => t.tabId === 'tab1')).toBeUndefined()
  })

  // Regression: covers tabs that land on a descendant of a live
  // floating window's root (e.g. an inner-tree split inside a popped-
  // out window). resolveTileWorkspace must walk parent_id up to the
  // floating-window root and find it registered.
  it('renders a tab whose tile descends from a live floating window root', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'mainRoot' } as never
    applyOp(state, setNodeKind('mainRoot', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    // Floating window with a SPLIT root and a LEAF child.
    applyOp(state, setNodeKind('fwRoot', NodeKind.SPLIT, hlc(2n, 0n, 'a')))
    applyOp(state, setNodeKind('fwLeaf', NodeKind.LEAF, hlc(2n, 1n, 'a')))
    applyOp(state, setNodeParentId('fwLeaf', 'fwRoot', hlc(2n, 2n, 'a')))
    applyOp(state, setNodePosition('fwLeaf', 'N', hlc(2n, 3n, 'a')))
    state.floatingWindows.fw1 = {
      $typeName: 'leapmux.v1.FloatingWindowRecord',
      windowId: 'fw1',
      rootNodeId: 'fwRoot',
      workspaceId: { $typeName: 'leapmux.v1.StringRegister', value: 'w1' },
    } as never
    applyOp(state, setTabTileId('tab1', 'fwLeaf', hlc(3n, 0n, 'a')))

    const proj = project(state)
    const rendered = proj.renderedTabs.find(t => t.tabId === 'tab1')
    // Single-child SPLIT collapses, so fwLeaf's tile_id stays valid
    // (resolveTileWorkspace walks to fwRoot). The tab is rendered.
    expect(rendered).toBeDefined()
    expect(rendered!.workspaceId).toBe('w1')
  })
})

describe('crdt projectWorkspace', () => {
  it('returns the same WorkspaceProjection shape as project() for the named workspace', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'root1' } as never
    state.workspaces.w2 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w2', rootNodeId: 'root2' } as never
    applyOp(state, setNodeKind('root1', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    applyOp(state, setNodeKind('root2', NodeKind.LEAF, hlc(2n, 0n, 'a')))

    const full = project(state).workspaces.get('w1')!
    const narrow = projectWorkspace(state, 'w1')!
    expect(narrow.workspaceId).toBe(full.workspaceId)
    expect(narrow.mainTree.nodeId).toBe(full.mainTree.nodeId)
    expect(narrow.mainTree.kind).toBe(full.mainTree.kind)
  })

  it('returns undefined for unknown workspace_id', () => {
    const state = newState('org')
    expect(projectWorkspace(state, 'no-such')).toBeUndefined()
  })

  it('includes only floating windows that belong to the named workspace', () => {
    const state = newState('org')
    state.workspaces.w1 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w1', rootNodeId: 'root1' } as never
    state.workspaces.w2 = { $typeName: 'leapmux.v1.WorkspaceContentsRecord', workspaceId: 'w2', rootNodeId: 'root2' } as never
    applyOp(state, setNodeKind('root1', NodeKind.LEAF, hlc(1n, 0n, 'a')))
    applyOp(state, setNodeKind('root2', NodeKind.LEAF, hlc(2n, 0n, 'a')))
    applyOp(state, setNodeKind('fwRoot', NodeKind.LEAF, hlc(3n, 0n, 'a')))
    // Attach a floating window to w2 only.
    state.floatingWindows.fw1 = {
      $typeName: 'leapmux.v1.FloatingWindowRecord',
      windowId: 'fw1',
      rootNodeId: 'fwRoot',
      workspaceId: { $typeName: 'leapmux.v1.StringRegister', value: 'w2' },
    } as never

    expect(projectWorkspace(state, 'w1')!.floatingWindows).toHaveLength(0)
    expect(projectWorkspace(state, 'w2')!.floatingWindows).toHaveLength(1)
  })
})
