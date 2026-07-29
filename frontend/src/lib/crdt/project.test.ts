import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  HLCSchema,
  NodeKind,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/user_crdt_pb'
import {
  CrdtOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneNodeOpSchema,
  TombstoneWorkspaceOpSchema,
} from '~/generated/leapmux/v1/user_ops_pb'
import { SplitDirection, TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'
import { project } from './project'

function hlc(p: bigint, l: bigint, c: string) {
  return create(HLCSchema, { physical: p, logical: l, clientId: c })
}

function setNodeKindOp(nodeId: string, kind: NodeKind, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'kind', value: kind } }) },
  })
}
function setNodeParentOp(nodeId: string, parentId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'parentId', value: parentId } }) },
  })
}
function setNodeDirOp(nodeId: string, direction: SplitDirection, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'direction', value: direction } }) },
  })
}
function setNodePosOp(nodeId: string, position: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'position', value: position } }) },
  })
}
function setNodeRowsOp(nodeId: string, rows: number, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'rows', value: rows } }) },
  })
}
function setNodeColsOp(nodeId: string, cols: number, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'cols', value: cols } }) },
  })
}
function setFwRootOp(windowId: string, rootNodeId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, { windowId, field: { case: 'rootNodeId', value: rootNodeId } }) },
  })
}
function setFwWorkspaceOp(windowId: string, workspaceId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, { windowId, field: { case: 'workspaceId', value: workspaceId } }) },
  })
}
function tombstoneWorkspaceOp(workspaceId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneWorkspace', value: create(TombstoneWorkspaceOpSchema, { workspaceId }) },
  })
}
function tombstoneNodeOp(nodeId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneNode', value: create(TombstoneNodeOpSchema, { nodeId }) },
  })
}
function setTabTileOp(tabId: string, tileId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, { tabType: TabType.AGENT, tabId, field: { case: 'tileId', value: tileId } }) },
  })
}

function seedRoot(workspaceId: string, rootId: string) {
  const state = newState('user')
  state.workspaces[workspaceId] = create(WorkspaceContentsRecordSchema, { workspaceId, rootNodeId: rootId })
  applyOp(state, setNodeKindOp(rootId, NodeKind.LEAF, 1n, 0n, 'seed'))
  return state
}

describe('project', () => {
  it('skips tombstoned children', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('child', NodeKind.LEAF, 2n, 0n, 'a'))
    applyOp(state, setNodeParentOp('child', 'root', 2n, 1n, 'a'))
    applyOp(state, tombstoneNodeOp('child', 3n, 0n, 'a'))
    const proj = project(state)
    const ws = proj.workspaces.get('w1')
    expect(ws?.mainTree.children.length).toBe(0)
  })

  it('drops orphans whose parent_id chain doesn\'t terminate at a registered root', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('orphan', NodeKind.LEAF, 2n, 0n, 'a'))
    applyOp(state, setNodeParentOp('orphan', 'ghost', 2n, 1n, 'a'))
    applyOp(state, setTabTileOp('t1', 'orphan', 3n, 0n, 'a'))
    const proj = project(state)
    expect(proj.renderedTabs.find(t => t.tabId === 't1')).toBeUndefined()
    expect(proj.ownedTabs.find(t => t.tabId === 't1')).toBeUndefined()
  })

  it('sPLIT with one live child renders as that child, keeping the child own id', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('root', NodeKind.SPLIT, 2n, 0n, 'a'))
    applyOp(state, setNodeDirOp('root', SplitDirection.HORIZONTAL, 2n, 1n, 'a'))
    applyOp(state, setNodeKindOp('child', NodeKind.LEAF, 3n, 0n, 'a'))
    applyOp(state, setNodeParentOp('child', 'root', 3n, 1n, 'a'))
    applyOp(state, setNodePosOp('child', 'N', 3n, 2n, 'a'))
    const proj = project(state)
    const ws = proj.workspaces.get('w1')
    // The rendered surface is the CHILD, under the child's own node id.
    // Re-keying it to the SPLIT's id used to give one surface two identities --
    // the leaf's real, writable CRDT id and the ancestor's -- which left tabs
    // anchored to a tile the tree no longer advertised.
    expect(ws?.mainTree.nodeId).toBe('child')
    expect(ws?.mainTree.kind).toBe(NodeKind.LEAF)
  })

  it('a tab whose tile resolves to a live leaf renders in both views', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setTabTileOp('t1', 'root', 5n, 0n, 'a'))
    const proj = project(state)
    expect(proj.ownedTabs.length).toBe(1)
    expect(proj.renderedTabs.length).toBe(1)
    expect(proj.renderedTabs[0].workspaceId).toBe('w1')
  })
})

/**
 * The invariant that ties the projection's two halves together.
 *
 * `renderedTabs` says "this tab is on screen"; `mainTree` says what is on
 * screen. If a tab's `tileId` names no leaf in its workspace's tree, the tab is
 * on screen nowhere — alive, untombstoned, and invisible. That is not a
 * hypothetical: the single-child SPLIT collapse used to re-key the surviving
 * leaf to its parent's id, and every tab anchored to that leaf silently
 * vanished until the user split the tile again.
 *
 * Asserting it as a property over many shapes, rather than case by case, is
 * what makes it hold for shapes nobody thought to write a test for. Every new
 * projection rule has to keep it.
 */
function leafIdsOf(tree: { nodeId: string, kind: NodeKind, children: unknown[] }): string[] {
  const out: string[] = []
  const walk = (n: { nodeId: string, kind: NodeKind, children: unknown[] }) => {
    if (n.children.length === 0) {
      if (n.nodeId !== '')
        out.push(n.nodeId)
      return
    }
    for (const c of n.children)
      walk(c as typeof n)
  }
  walk(tree)
  return out
}

/** Every rendered tab must name a leaf that its workspace's tree actually shows. */
function assertRenderedTabsHaveATile(state: ReturnType<typeof newState>, label: string) {
  const proj = project(state)
  for (const tab of proj.renderedTabs) {
    const ws = proj.workspaces.get(tab.workspaceId)
    expect(ws, `${label}: rendered tab ${tab.tabId} names unknown workspace ${tab.workspaceId}`).toBeDefined()
    const leaves = new Set([
      ...leafIdsOf(ws!.mainTree),
      ...ws!.floatingWindows.flatMap(w => leafIdsOf(w.innerTree)),
    ])
    expect(
      leaves.has(tab.tileId),
      `${label}: rendered tab ${tab.tabId} sits on tile ${tab.tileId}, which is not a leaf in the rendered tree (leaves: ${[...leaves].join(', ')})`,
    ).toBe(true)
  }
  // And the rendered set never invents a tile: it reports the tab's own
  // `tile_id` register, which is the addressable id every write path uses.
  for (const tab of proj.renderedTabs)
    expect(tab.tileId, `${label}: rendered tab ${tab.tabId} was relabelled`).toBe(state.tabs[tab.tabId]?.tileId?.value)
}

describe('projection invariant: every rendered tab has a tile to render on', () => {
  /** Build `root` as a SPLIT over two fresh leaves, with a tab on the first. */
  function splitRootWithTab(tabTile: 'childA' | 'childB' = 'childA') {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('root', NodeKind.SPLIT, 2n, 0n, 'a'))
    applyOp(state, setNodeKindOp('childA', NodeKind.LEAF, 3n, 0n, 'a'))
    applyOp(state, setNodeParentOp('childA', 'root', 3n, 1n, 'a'))
    applyOp(state, setNodePosOp('childA', 'N', 3n, 2n, 'a'))
    applyOp(state, setNodeKindOp('childB', NodeKind.LEAF, 4n, 0n, 'a'))
    applyOp(state, setNodeParentOp('childB', 'root', 4n, 1n, 'a'))
    applyOp(state, setNodePosOp('childB', 'V', 4n, 2n, 'a'))
    applyOp(state, setTabTileOp('t1', tabTile, 5n, 0n, 'a'))
    return state
  }

  it('holds for a plain two-child split', () => {
    assertRenderedTabsHaveATile(splitRootWithTab(), 'two-child split')
  })

  // The shape that broke: one sibling goes, the SPLIT has a single live child,
  // and the collapse decides what the survivor is called.
  it('holds after the sibling is tombstoned and the split collapses', () => {
    const state = splitRootWithTab()
    applyOp(state, tombstoneNodeOp('childB', 6n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'collapsed split')
  })

  it('holds when the tab is on the tile that survives, whichever side that is', () => {
    const onB = splitRootWithTab('childB')
    applyOp(onB, tombstoneNodeOp('childA', 6n, 0n, 'a'))
    assertRenderedTabsHaveATile(onB, 'collapsed split, tab on the second child')
  })

  // Collapses nest. A chain that renames twice is where a
  // resolve-one-level-only fix would pass the simple case and still strand
  // tabs.
  it('holds through a nested single-child chain', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('root', NodeKind.SPLIT, 2n, 0n, 'a'))
    applyOp(state, setNodeKindOp('inner', NodeKind.SPLIT, 3n, 0n, 'a'))
    applyOp(state, setNodeParentOp('inner', 'root', 3n, 1n, 'a'))
    applyOp(state, setNodePosOp('inner', 'N', 3n, 2n, 'a'))
    applyOp(state, setNodeKindOp('outerSib', NodeKind.LEAF, 4n, 0n, 'a'))
    applyOp(state, setNodeParentOp('outerSib', 'root', 4n, 1n, 'a'))
    applyOp(state, setNodePosOp('outerSib', 'V', 4n, 2n, 'a'))
    applyOp(state, setNodeKindOp('deep', NodeKind.LEAF, 5n, 0n, 'a'))
    applyOp(state, setNodeParentOp('deep', 'inner', 5n, 1n, 'a'))
    applyOp(state, setNodePosOp('deep', 'N', 5n, 2n, 'a'))
    applyOp(state, setNodeKindOp('innerSib', NodeKind.LEAF, 6n, 0n, 'a'))
    applyOp(state, setNodeParentOp('innerSib', 'inner', 6n, 1n, 'a'))
    applyOp(state, setNodePosOp('innerSib', 'V', 6n, 2n, 'a'))
    applyOp(state, setTabTileOp('t1', 'deep', 7n, 0n, 'a'))

    applyOp(state, tombstoneNodeOp('innerSib', 8n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'one level collapsed')
    applyOp(state, tombstoneNodeOp('outerSib', 9n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'two levels collapsed')
  })

  it('holds for a tab in a grid cell', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('root', NodeKind.GRID, 2n, 0n, 'a'))
    applyOp(state, setNodeRowsOp('root', 1, 2n, 1n, 'a'))
    applyOp(state, setNodeColsOp('root', 2, 2n, 2n, 'a'))
    applyOp(state, setNodeKindOp('cell00', NodeKind.LEAF, 3n, 0n, 'a'))
    applyOp(state, setNodeParentOp('cell00', 'root', 3n, 1n, 'a'))
    applyOp(state, setNodePosOp('cell00', '0,0', 3n, 2n, 'a'))
    applyOp(state, setNodeKindOp('cell01', NodeKind.LEAF, 4n, 0n, 'a'))
    applyOp(state, setNodeParentOp('cell01', 'root', 4n, 1n, 'a'))
    applyOp(state, setNodePosOp('cell01', '0,1', 4n, 2n, 'a'))
    applyOp(state, setTabTileOp('t1', 'cell00', 5n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'grid cell')
  })

  it('holds for a bare root leaf', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setTabTileOp('t1', 'root', 2n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'root leaf')
  })

  it('holds for a tab inside a floating window', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('fwRoot', NodeKind.LEAF, 3n, 0n, 'a'))
    applyOp(state, setFwRootOp('fw1', 'fwRoot', 4n, 0n, 'a'))
    applyOp(state, setFwWorkspaceOp('fw1', 'w1', 4n, 1n, 'a'))
    applyOp(state, setTabTileOp('t1', 'fwRoot', 5n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'floating window')
  })

  // The tree already drops a window whose workspace record is gone, so if tab
  // resolution does not drop it too, the tab is reported as rendering in a
  // workspace the projection does not even contain.
  it('holds when a floating window\'s workspace has been tombstoned', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('fwRoot', NodeKind.LEAF, 3n, 0n, 'a'))
    applyOp(state, setFwRootOp('fw1', 'fwRoot', 4n, 0n, 'a'))
    applyOp(state, setFwWorkspaceOp('fw1', 'w1', 4n, 1n, 'a'))
    applyOp(state, setTabTileOp('t1', 'fwRoot', 5n, 0n, 'a'))
    applyOp(state, tombstoneWorkspaceOp('w1', 6n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'floating window whose workspace is gone')
  })

  // A tombstoned root is only reachable via the internal lifecycle path, which
  // bypasses `root_node_protected`. The tab must leave BOTH views, exactly as
  // it would for any other tombstoned tile.
  it('holds when the workspace root node itself is tombstoned', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setTabTileOp('t1', 'root', 2n, 0n, 'a'))
    applyOp(state, tombstoneNodeOp('root', 3n, 0n, 'a'))
    assertRenderedTabsHaveATile(state, 'tombstoned workspace root')
  })
})

/**
 * The Go mirror memoizes tile resolution per `Project` call, with the comment
 * "so multi-tab leaves don't re-walk identical parent chains" — and the client
 * needs it MORE than the hub does: `project()` re-runs on every CRDT tick
 * (~60/s while a tile is dragged), where the hub runs it once per commit.
 */
describe('tile resolution is memoized per call', () => {
  function chainWithTabs(tabCount: number) {
    // L -> B -> A -> R(registered root); every tab sits on L.
    const state = seedRoot('w1', 'R')
    applyOp(state, setNodeKindOp('A', NodeKind.SPLIT, 1n, 1n, 'a'))
    applyOp(state, setNodeParentOp('A', 'R', 1n, 2n, 'a'))
    applyOp(state, setNodeKindOp('B', NodeKind.SPLIT, 1n, 3n, 'a'))
    applyOp(state, setNodeParentOp('B', 'A', 1n, 4n, 'a'))
    applyOp(state, setNodeKindOp('L', NodeKind.LEAF, 1n, 5n, 'a'))
    applyOp(state, setNodeParentOp('L', 'B', 1n, 6n, 'a'))
    for (let i = 0; i < tabCount; i++)
      applyOp(state, setTabTileOp(`t${i}`, 'L', 2n, BigInt(i), 'a'))
    return state
  }

  /** Count reads of `state.nodes[...]` during one `project()` call. */
  function nodeLookups(tabCount: number): number {
    const state = chainWithTabs(tabCount)
    let gets = 0
    state.nodes = new Proxy(state.nodes, {
      get(target, prop, recv) {
        if (typeof prop === 'string')
          gets++
        return Reflect.get(target, prop, recv)
      },
    })
    expect(project(state).renderedTabs).toHaveLength(tabCount)
    return gets
  }

  it('walks a shared tile chain once, however many tabs sit on it', () => {
    // Each extra tab may cost at most ONE lookup — its own `tileIsLeaf`. The
    // 4-deep chain walk is paid once for the tile they share. Without the memo
    // each tab re-walks it, so the delta is ~5x this.
    const delta = nodeLookups(10) - nodeLookups(1)
    expect(delta).toBeLessThanOrEqual(9)
  })

  it('still resolves every tab correctly through the memo', () => {
    const proj = project(chainWithTabs(3))
    expect(proj.ownedTabs.map(t => t.tabId)).toEqual(['t0', 't1', 't2'])
    expect(proj.ownedTabs.every(t => t.workspaceId === 'w1' && t.tileId === 'L')).toBe(true)
  })
})
