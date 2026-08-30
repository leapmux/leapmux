import type { MessageInitShape } from '@bufbuild/protobuf'
import type { NodeRegisterKey } from './project'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  DoubleListSchema,
  HLCSchema,
  NodeKind,
  WorkspaceContentsRecordSchema,
} from '~/generated/proto/leapmux/v1/user_crdt_pb'
import {
  CrdtOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  SetWorkspaceRegisterOpSchema,
  SetWorkspaceRootNodeOpSchema,
  TombstoneFloatingWindowOpSchema,
  TombstoneNodeOpSchema,
  TombstoneTabOpSchema,
  TombstoneWorkspaceOpSchema,
} from '~/generated/proto/leapmux/v1/user_ops_pb'
import { SplitDirection, TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'
import { project, ProjectionCache } from './project'

function hlc(p: bigint, l: bigint, c: string) {
  return create(HLCSchema, { physical: p, logical: l, clientId: c })
}

// One builder per register FAMILY rather than per field: the three families each
// had a stack of byte-identical wrappers differing only in the oneof case, and a
// new field meant another copy. The named helpers below stay -- they read better
// at a call site than an inline oneof literal -- but they no longer repeat the
// envelope.
type NodeField = MessageInitShape<typeof SetNodeRegisterOpSchema>['field']
type TabField = MessageInitShape<typeof SetTabRegisterOpSchema>['field']
type FwField = MessageInitShape<typeof SetFloatingWindowRegisterOpSchema>['field']

function setNodeOp(nodeId: string, field: NodeField, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field }) },
  })
}
function setTabOp(tabId: string, field: TabField, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, { tabType: TabType.AGENT, tabId, field }) },
  })
}
function setFwOp(windowId: string, field: FwField, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setFloatingWindowRegister', value: create(SetFloatingWindowRegisterOpSchema, { windowId, field }) },
  })
}
function doubles(values: number[]) {
  return create(DoubleListSchema, { values })
}

function setNodeKindOp(nodeId: string, kind: NodeKind, p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'kind', value: kind }, p, l, c)
}
function setNodeParentOp(nodeId: string, parentId: string, p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'parentId', value: parentId }, p, l, c)
}
function setNodeDirOp(nodeId: string, direction: SplitDirection, p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'direction', value: direction }, p, l, c)
}
function setNodePosOp(nodeId: string, position: string, p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'position', value: position }, p, l, c)
}
function setNodeRowsOp(nodeId: string, rows: number, p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'rows', value: rows }, p, l, c)
}
function setNodeColsOp(nodeId: string, cols: number, p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'cols', value: cols }, p, l, c)
}
function setNodeRatiosOp(nodeId: string, ratios: number[], p: bigint, l: bigint, c: string) {
  return setNodeOp(nodeId, { case: 'ratios', value: doubles(ratios) }, p, l, c)
}
function setFwRootOp(windowId: string, rootNodeId: string, p: bigint, l: bigint, c: string) {
  return setFwOp(windowId, { case: 'rootNodeId', value: rootNodeId }, p, l, c)
}
function setFwWorkspaceOp(windowId: string, workspaceId: string, p: bigint, l: bigint, c: string) {
  return setFwOp(windowId, { case: 'workspaceId', value: workspaceId }, p, l, c)
}
function setFwXOp(windowId: string, x: number, p: bigint, l: bigint, c: string) {
  return setFwOp(windowId, { case: 'x', value: x }, p, l, c)
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
function tombstoneTabOp(tabId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneTab', value: create(TombstoneTabOpSchema, { tabType: TabType.AGENT, tabId }) },
  })
}
function tombstoneFwOp(windowId: string, p: bigint, l: bigint, c: string) {
  return create(CrdtOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneFloatingWindow', value: create(TombstoneFloatingWindowOpSchema, { windowId }) },
  })
}
function setTabTileOp(tabId: string, tileId: string, p: bigint, l: bigint, c: string) {
  return setTabOp(tabId, { case: 'tileId', value: tileId }, p, l, c)
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

  it('a SPLIT with one live child renders as that child, keeping the child\'s own id', () => {
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
function leafIdsOf(tree: { nodeId: string, kind: NodeKind, children: readonly unknown[] }): string[] {
  const out: string[] = []
  const walk = (n: { nodeId: string, kind: NodeKind, children: readonly unknown[] }) => {
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
 * needs it MORE than the hub does: `project()` re-runs on every CRDT tick (~2x
 * the frame rate while a tile is dragged, since each frame's optimistic submit
 * is followed by its commit echo), where the hub runs it once per commit.
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

/**
 * `ProjectionCache` decides what gets ALLOCATED, never what is returned. Two
 * things have to be pinned, and they need different kinds of test:
 *
 *   - it agrees with the uncached `project(state)` on every state. That is a
 *     DIFFERENTIAL property, and `conformance.test.ts` already replays every
 *     shared fixture case through both paths op by op -- but those fixtures
 *     exist for the client/hub tab-ownership contract and contain no `ratios`,
 *     `direction`, `rowRatios`, `colRatios` or floating-window geometry op at
 *     all. Which is to say: they cover none of the registers a DRAG writes, the
 *     exact hot path this cache exists for. Verified by deleting a register from
 *     the comparison -- the whole conformance suite stayed green. Hence the fuzz
 *     below, which emits every register in every family.
 *
 *   - it actually reuses. The performance claim is not testable as a duration
 *     without flakiness, but the object identities it rests on are exact, so the
 *     reuse cases assert those instead.
 */
describe('projection cache', () => {
  /**
   * xorshift32. Seeded per case and never `Math.random`: a differential test
   * that cannot be replayed from its failure output is barely a test.
   */
  function makeRng(seed: number): () => number {
    let s = seed >>> 0 || 0x9E3779B9
    return () => {
      s ^= s << 13
      s >>>= 0
      s ^= s >>> 17
      s ^= s << 5
      s >>>= 0
      return s / 0x100000000
    }
  }

  function setWorkspaceRootOp(workspaceId: string, rootNodeId: string, p: bigint, l: bigint, c: string) {
    return create(CrdtOpSchema, {
      canonicalHlc: hlc(p, l, c),
      body: { case: 'setWorkspaceRootNode', value: create(SetWorkspaceRootNodeOpSchema, { workspaceId, rootNodeId }) },
    })
  }
  function setWorkspaceOp(workspaceId: string, p: bigint, l: bigint, c: string) {
    return create(CrdtOpSchema, {
      canonicalHlc: hlc(p, l, c),
      body: { case: 'setWorkspaceRegister', value: create(SetWorkspaceRegisterOpSchema, { workspaceId }) },
    })
  }

  const FUZZ_WORKSPACES = ['wA', 'wB', 'wC'] as const
  const FUZZ_NODES = ['n0', 'n1', 'n2', 'n3', 'n4', 'n5'] as const
  const FUZZ_TABS = ['t0', 't1', 't2', 't3'] as const
  const FUZZ_WINDOWS = ['f0', 'f1'] as const
  const FUZZ_KINDS = [NodeKind.LEAF, NodeKind.SPLIT, NodeKind.GRID] as const
  const FUZZ_RATIOS = [[], [0.5, 0.5], [0.25, 0.75], [1, 2, 3], [-1, 0.5]] as const
  const FUZZ_POSITIONS = ['', 'N', 'V', 'a', 'z', '0,0', '0,1', '1,0', '1,1'] as const

  /**
   * Every op kind the projection reads, including the ones no shared fixture
   * covers.
   *
   * Weighted, and it has to be: tombstones are permanent and there are only a
   * handful of entities, so a uniform draw razes the state within the first few
   * dozen ops and every projection after that is empty. The coverage counters at
   * the end of the test are what keep that from passing silently.
   */
  const FUZZ_OPS: Array<{ weight: number, make: (rand: () => number, seq: number) => ReturnType<typeof setNodeKindOp> }> = [
    { weight: 8, make: (r, s) => setNodeKindOp(pick(r, FUZZ_NODES), pick(r, FUZZ_KINDS), 1n, BigInt(s), 'z') },
    { weight: 8, make: (r, s) => setNodeParentOp(pick(r, FUZZ_NODES), pick(r, [...FUZZ_NODES, 'ghost', '']), 1n, BigInt(s), 'z') },
    { weight: 6, make: (r, s) => setNodePosOp(pick(r, FUZZ_NODES), pick(r, FUZZ_POSITIONS), 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setNodeDirOp(pick(r, FUZZ_NODES), pick(r, [SplitDirection.HORIZONTAL, SplitDirection.VERTICAL]), 1n, BigInt(s), 'z') },
    { weight: 6, make: (r, s) => setNodeRatiosOp(pick(r, FUZZ_NODES), [...pick(r, FUZZ_RATIOS)], 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setNodeRowsOp(pick(r, FUZZ_NODES), Math.floor(r() * 4), 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setNodeColsOp(pick(r, FUZZ_NODES), Math.floor(r() * 4), 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setNodeOp(pick(r, FUZZ_NODES), { case: 'rowRatios', value: doubles([...pick(r, FUZZ_RATIOS)]) }, 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setNodeOp(pick(r, FUZZ_NODES), { case: 'colRatios', value: doubles([...pick(r, FUZZ_RATIOS)]) }, 1n, BigInt(s), 'z') },
    { weight: 1, make: (r, s) => tombstoneNodeOp(pick(r, FUZZ_NODES), 1n, BigInt(s), 'z') },
    { weight: 8, make: (r, s) => setTabTileOp(pick(r, FUZZ_TABS), pick(r, [...FUZZ_NODES, 'ghost', '']), 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setTabOp(pick(r, FUZZ_TABS), { case: 'position', value: pick(r, FUZZ_POSITIONS) }, 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setTabOp(pick(r, FUZZ_TABS), { case: 'workerId', value: pick(r, ['', 'w-1', 'w-2']) }, 1n, BigInt(s), 'z') },
    { weight: 1, make: (r, s) => tombstoneTabOp(pick(r, FUZZ_TABS), 1n, BigInt(s), 'z') },
    { weight: 6, make: (r, s) => setFwRootOp(pick(r, FUZZ_WINDOWS), pick(r, [...FUZZ_NODES, 'ghost']), 1n, BigInt(s), 'z') },
    { weight: 6, make: (r, s) => setFwWorkspaceOp(pick(r, FUZZ_WINDOWS), pick(r, [...FUZZ_WORKSPACES, 'gone']), 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setFwOp(pick(r, FUZZ_WINDOWS), { case: 'x', value: r() * 100 }, 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setFwOp(pick(r, FUZZ_WINDOWS), { case: 'y', value: r() * 100 }, 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setFwOp(pick(r, FUZZ_WINDOWS), { case: 'width', value: r() * 500 }, 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setFwOp(pick(r, FUZZ_WINDOWS), { case: 'height', value: r() * 500 }, 1n, BigInt(s), 'z') },
    { weight: 4, make: (r, s) => setFwOp(pick(r, FUZZ_WINDOWS), { case: 'opacity', value: r() }, 1n, BigInt(s), 'z') },
    { weight: 1, make: (r, s) => tombstoneFwOp(pick(r, FUZZ_WINDOWS), 1n, BigInt(s), 'z') },
    { weight: 3, make: (r, s) => setWorkspaceOp(pick(r, FUZZ_WORKSPACES), 1n, BigInt(s), 'z') },
    { weight: 3, make: (r, s) => setWorkspaceRootOp(pick(r, FUZZ_WORKSPACES), pick(r, FUZZ_NODES), 1n, BigInt(s), 'z') },
    { weight: 1, make: (r, s) => tombstoneWorkspaceOp(pick(r, FUZZ_WORKSPACES), 1n, BigInt(s), 'z') },
  ]
  const FUZZ_TOTAL_WEIGHT = FUZZ_OPS.reduce((n, o) => n + o.weight, 0)

  /**
   * Seeds, and how often a step redacts a node record instead of applying an op.
   *
   * Both are tuned against the coverage counters at the end of the test, not
   * picked round. Redaction is the only source of topology churn (see the note
   * at its call site) but it also razes the tree it churns: at 0.06 the run
   * stopped producing any node with children at all, which is exactly the
   * coverage it exists to add. 0.03 over 20 seeds keeps all six counters
   * healthy.
   */
  const FUZZ_SEEDS = 20
  const REDACTION_RATE = 0.03

  function pick<T>(rand: () => number, xs: readonly T[]): T {
    return xs[Math.floor(rand() * xs.length)]
  }

  function pickWeighted(rand: () => number) {
    let n = rand() * FUZZ_TOTAL_WEIGHT
    for (const op of FUZZ_OPS) {
      n -= op.weight
      if (n < 0)
        return op
    }
    return FUZZ_OPS[FUZZ_OPS.length - 1]
  }

  it('agrees with the uncached projection after every op, across every register', () => {
    // Coverage counters, asserted at the end: a fuzz that only ever produced
    // empty projections would pass on nothing at all.
    let sawRenderedTab = 0
    let sawTreeChild = 0
    let sawWindow = 0
    let sawEmptyGridCell = 0
    let sawRedaction = 0
    let sawReparent = 0

    for (let seed = 1; seed <= FUZZ_SEEDS; seed++) {
      const rand = makeRng(seed)
      const state = newState('user')
      // Two workspaces exist up front so the very first ops land somewhere; the
      // third is only reachable through `setWorkspaceRegister`.
      for (const wsId of ['wA', 'wB'])
        state.workspaces[wsId] = create(WorkspaceContentsRecordSchema, { workspaceId: wsId, rootNodeId: wsId === 'wA' ? 'n0' : 'n1' })
      const cache = new ProjectionCache()
      // Last non-empty parent seen per node, so a move can be counted ACROSS a
      // redaction. It cannot be spotted on a live record: `parentId` is
      // set-once, so the only way a node changes parent is record-gone then
      // re-attached.
      const lastParent = new Map<string, string>()
      for (let seq = 1; seq <= 150; seq++) {
        let label: string
        // Node redaction: `consumeEntityRemoved` deletes the record outright
        // rather than tombstoning it, and it is not expressible as a CrdtOp.
        //
        // It is also the only thing that lets this fuzz move TOPOLOGY. `parentId`
        // is set-once (`apply.ts` writes it only while it is `""`), so with a
        // fixed node pool every node's parent is frozen by its first successful
        // write, and `CachedNode.children` -- the comparison that catches "a
        // descendant changed but my own registers did not" -- would go unexercised
        // after the opening ops. Deleting the record clears the set-once, so a
        // later parent op re-attaches the node somewhere else.
        if (rand() < REDACTION_RATE) {
          const nodeId = pick(rand, FUZZ_NODES)
          label = `redact ${nodeId}`
          if (state.nodes[nodeId]) {
            delete state.nodes[nodeId]
            sawRedaction++
          }
        }
        else {
          const op = pickWeighted(rand).make(rand, seq)
          label = String(op.body.case)
          applyOp(state, op)
        }
        for (const nodeId of FUZZ_NODES) {
          const parent = state.nodes[nodeId]?.parentId ?? ''
          if (parent === '')
            continue
          const before = lastParent.get(nodeId)
          if (before !== undefined && before !== parent)
            sawReparent++
          lastParent.set(nodeId, parent)
        }
        const cached = project(state, cache)
        expect(
          cached,
          `seed ${seed}: cached projection diverged after op ${seq} (${label})`,
        ).toEqual(project(state))
        sawRenderedTab += cached.renderedTabs.length
        for (const ws of cached.workspaces.values()) {
          sawTreeChild += ws.mainTree.children.length
          sawWindow += ws.floatingWindows.length
          sawEmptyGridCell += ws.mainTree.children.filter(c => c.nodeId === '').length
        }
      }
    }

    expect(sawRenderedTab, 'fuzz never rendered a tab').toBeGreaterThan(0)
    expect(sawTreeChild, 'fuzz never built a tree with children').toBeGreaterThan(0)
    expect(sawWindow, 'fuzz never projected a floating window').toBeGreaterThan(0)
    expect(sawEmptyGridCell, 'fuzz never produced a virtual empty grid cell').toBeGreaterThan(0)
    expect(sawRedaction, 'fuzz never redacted a node record').toBeGreaterThan(0)
    // Without this one the run is register churn over a frozen tree, which is
    // exactly the shape that leaves `CachedNode.children` untested.
    expect(sawReparent, 'fuzz never moved a node to a different parent').toBeGreaterThan(0)
  })

  // --- reuse ---

  /**
   * Two workspaces, each a two-child SPLIT with one tab per child, plus a
   * floating window in `wA`. Enough that "changed one thing" and "left the rest
   * alone" are distinguishable.
   */
  function twoWorkspaces() {
    const state = newState('user')
    state.workspaces.wA = create(WorkspaceContentsRecordSchema, { workspaceId: 'wA', rootNodeId: 'rootA' })
    state.workspaces.wB = create(WorkspaceContentsRecordSchema, { workspaceId: 'wB', rootNodeId: 'rootB' })
    let seq = 0
    const next = () => BigInt(++seq)
    for (const ws of ['A', 'B'] as const) {
      applyOp(state, setNodeKindOp(`root${ws}`, NodeKind.SPLIT, 1n, next(), 'a'))
      applyOp(state, setNodeRatiosOp(`root${ws}`, [0.5, 0.5], 1n, next(), 'a'))
      for (const side of ['1', '2'] as const) {
        const leaf = `leaf${ws}${side}`
        applyOp(state, setNodeKindOp(leaf, NodeKind.LEAF, 1n, next(), 'a'))
        applyOp(state, setNodeParentOp(leaf, `root${ws}`, 1n, next(), 'a'))
        applyOp(state, setNodePosOp(leaf, side === '1' ? 'N' : 'V', 1n, next(), 'a'))
        applyOp(state, setTabTileOp(`tab${ws}${side}`, leaf, 1n, next(), 'a'))
      }
    }
    applyOp(state, setNodeKindOp('fwRoot', NodeKind.LEAF, 1n, next(), 'a'))
    applyOp(state, setFwRootOp('fw1', 'fwRoot', 1n, next(), 'a'))
    applyOp(state, setFwWorkspaceOp('fw1', 'wA', 1n, next(), 'a'))
    applyOp(state, setFwXOp('fw1', 10, 1n, next(), 'a'))
    return { state, next }
  }

  const rowFor = (proj: ReturnType<typeof project>, tabId: string) =>
    proj.renderedTabs.find(t => t.tabId === tabId)

  it('returns the identical projection when the tick changed nothing', () => {
    const { state } = twoWorkspaces()
    const cache = new ProjectionCache()
    const first = project(state, cache)
    // This is the case that dominates in the app: the `BatchCommitted` that
    // follows every drag frame rebuilds `speculativeState` without changing what
    // the projection can see. Handing back the same object is what stops the
    // caller's memo -- and every memo downstream of it -- from re-running.
    expect(project(state, cache)).toBe(first)
    expect(project(state, cache)).toBe(first)
  })

  it('clear() drops the PENDING generation too, not just the committed one', () => {
    const { state } = twoWorkspaces()
    const cache = new ProjectionCache()
    project(state, cache)

    cache.clear()

    // Driven through `commit` because that is the only way the pending half is
    // observable: it ASSIGNS the four `next` twins onto the maps every lookup
    // reads, so between calls each pair is the same object and a `clear` that
    // replaced only one half leaves the other holding the whole last generation.
    // `begin` would mask it -- it replaces the twins on every `project()` -- and
    // the cost in production is retention rather than a wrong answer: the
    // signed-out tenant's `RenderTree`/`RenderedTab`/`RenderedFloatingWindow`
    // graph, plus every `LayoutNodeLocal` keyed off it, stays reachable for the
    // life of the page. That is exactly what `clear()` exists to prevent.
    cache.commit(new Map(), [], [], parts => ({ userId: 'next-tenant', ...parts }))

    let builds = 0
    const leaf = state.nodes.leafA1
    cache.tree('leafA1', leaf, [], () => {
      builds += 1
      return { nodeId: 'leafA1', kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
    })
    expect(builds, 'a cleared entry must be rebuilt, not served back from the pending twin').toBe(1)
  })

  it('a ratio drag in one workspace leaves every other workspace untouched', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    applyOp(state, setNodeRatiosOp('rootA', [0.3, 0.7], 1n, next(), 'a'))
    const after = project(state, cache)

    expect(after, 'the drag must propagate').not.toBe(before)
    expect(after.workspaces.get('wA')).not.toBe(before.workspaces.get('wA'))
    expect(after.workspaces.get('wA')?.mainTree.ratios).toEqual([0.3, 0.7])
    // The whole point: nothing else moves.
    expect(after.workspaces.get('wB')).toBe(before.workspaces.get('wB'))
    expect(after.ownedTabs).toBe(before.ownedTabs)
    expect(after.renderedTabs).toBe(before.renderedTabs)
    expect(after.workspaces.get('wA')?.floatingWindows).toBe(before.workspaces.get('wA')?.floatingWindows)
    // The dragged SPLIT is rebuilt; its children are not.
    expect(after.workspaces.get('wA')?.mainTree.children[0]).toBe(before.workspaces.get('wA')?.mainTree.children[0])
  })

  it('a floating-window drag rebuilds only that window', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    applyOp(state, setFwXOp('fw1', 42, 1n, next(), 'a'))
    const after = project(state, cache)

    const beforeWin = before.workspaces.get('wA')?.floatingWindows[0]
    const afterWin = after.workspaces.get('wA')?.floatingWindows[0]
    expect(afterWin).not.toBe(beforeWin)
    expect(afterWin?.x).toBe(42)
    // Geometry does not touch the window's inner tree, nor anything in wB.
    expect(afterWin?.innerTree).toBe(beforeWin?.innerTree)
    expect(after.workspaces.get('wA')?.mainTree).toBe(before.workspaces.get('wA')?.mainTree)
    expect(after.workspaces.get('wB')).toBe(before.workspaces.get('wB'))
    expect(after.renderedTabs).toBe(before.renderedTabs)
  })

  it('moving one tab rebuilds that row and no other', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    applyOp(state, setTabTileOp('tabA1', 'leafA2', 1n, next(), 'a'))
    const after = project(state, cache)

    expect(rowFor(after, 'tabA1')?.tileId).toBe('leafA2')
    expect(rowFor(after, 'tabA1')).not.toBe(rowFor(before, 'tabA1'))
    for (const id of ['tabA2', 'tabB1', 'tabB2'])
      expect(rowFor(after, id), id).toBe(rowFor(before, id))
    // A tab move is not a layout change.
    expect(after.workspaces).toBe(before.workspaces)
  })

  it('a metadata-free tick after a drag settles back to full reuse', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    project(state, cache)
    applyOp(state, setNodeRatiosOp('rootA', [0.4, 0.6], 1n, next(), 'a'))
    const dragged = project(state, cache)
    // The commit echo that follows re-projects the same state; it must be free.
    expect(project(state, cache)).toBe(dragged)
  })

  it.each([
    ['a kind flip', (s: ReturnType<typeof twoWorkspaces>) => applyOp(s.state, setNodeKindOp('leafA1', NodeKind.GRID, 1n, s.next(), 'b'))],
    ['a tombstoned node', (s: ReturnType<typeof twoWorkspaces>) => applyOp(s.state, tombstoneNodeOp('leafA1', 1n, s.next(), 'b'))],
    ['a new workspace root', (s: ReturnType<typeof twoWorkspaces>) => applyOp(s.state, setNodeParentOp('fwRoot', 'rootA', 1n, s.next(), 'b'))],
    ['a tombstoned window', (s: ReturnType<typeof twoWorkspaces>) => applyOp(s.state, tombstoneFwOp('fw1', 1n, s.next(), 'b'))],
    ['a tombstoned workspace', (s: ReturnType<typeof twoWorkspaces>) => applyOp(s.state, tombstoneWorkspaceOp('wA', 1n, s.next(), 'b'))],
    ['a tombstoned tab', (s: ReturnType<typeof twoWorkspaces>) => applyOp(s.state, tombstoneTabOp('tabA1', 1n, s.next(), 'b'))],
  ])('misses on %s, and still answers what the uncached projection would', (_label, mutate) => {
    const fixture = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(fixture.state, cache)
    mutate(fixture)
    const after = project(fixture.state, cache)
    expect(after).not.toBe(before)
    expect(after).toEqual(project(fixture.state))
  })

  it('moving a window between workspaces rebuilds both lists but keeps the window', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    applyOp(state, setFwWorkspaceOp('fw1', 'wB', 1n, next(), 'a'))
    const after = project(state, cache)

    expect(after.workspaces.get('wA')?.floatingWindows).toHaveLength(0)
    expect(after.workspaces.get('wB')?.floatingWindows).toHaveLength(1)
    // Nothing ABOUT the window changed, so the window object itself is reused --
    // `workspace_id` is not one of its fields.
    expect(after.workspaces.get('wB')?.floatingWindows[0]).toBe(before.workspaces.get('wA')?.floatingWindows[0])
  })

  // The one way a tab's row can change with every register on it untouched. It
  // is a real user action (drag a floating window onto another workspace), and
  // the only thing standing between it and a row that reports the OLD workspace
  // forever is `CachedTab.workspaceId`.
  it('re-resolves a tab inside a floating window that moves workspaces', () => {
    const { state, next } = twoWorkspaces()
    applyOp(state, setTabTileOp('fwTab', 'fwRoot', 1n, next(), 'a'))
    const cache = new ProjectionCache()
    const before = project(state, cache)
    expect(rowFor(before, 'fwTab')?.workspaceId).toBe('wA')

    applyOp(state, setFwWorkspaceOp('fw1', 'wB', 1n, next(), 'a'))
    const after = project(state, cache)
    expect(rowFor(after, 'fwTab')?.workspaceId).toBe('wB')
    expect(rowFor(after, 'fwTab')?.tileId).toBe('fwRoot')
    expect(after).toEqual(project(state))
    // Its neighbours in the main trees did not move.
    expect(rowFor(after, 'tabB1')).toBe(rowFor(before, 'tabB1'))
  })

  it('reuses a grid whose cells are not all filled', () => {
    const state = newState('user')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'root' })
    let seq = 0
    const next = () => BigInt(++seq)
    applyOp(state, setNodeKindOp('root', NodeKind.GRID, 1n, next(), 'a'))
    applyOp(state, setNodeRowsOp('root', 2, 1n, next(), 'a'))
    applyOp(state, setNodeColsOp('root', 2, 1n, next(), 'a'))
    // Three of four cells. The missing one renders as a virtual empty leaf,
    // which used to be a fresh object per call -- enough on its own to make
    // every grid with a hole miss forever.
    for (const [id, pos] of [['c00', '0,0'], ['c01', '0,1'], ['c10', '1,0']]) {
      applyOp(state, setNodeKindOp(id, NodeKind.LEAF, 1n, next(), 'a'))
      applyOp(state, setNodeParentOp(id, 'root', 1n, next(), 'a'))
      applyOp(state, setNodePosOp(id, pos, 1n, next(), 'a'))
    }
    const cache = new ProjectionCache()
    const first = project(state, cache)
    expect(first.workspaces.get('w1')?.mainTree.children).toHaveLength(4)
    expect(first.workspaces.get('w1')?.mainTree.children[3].nodeId).toBe('')
    expect(project(state, cache)).toBe(first)
  })

  it('answers correctly when the workspace root is inside a parent cycle', () => {
    const state = newState('user')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'root' })
    let seq = 0
    const next = () => BigInt(++seq)
    applyOp(state, setNodeKindOp('root', NodeKind.SPLIT, 1n, next(), 'a'))
    applyOp(state, setNodeKindOp('mid', NodeKind.SPLIT, 1n, next(), 'a'))
    applyOp(state, setNodeParentOp('mid', 'root', 1n, next(), 'a'))
    // ...and back up: `root` is now its own descendant.
    applyOp(state, setNodeParentOp('root', 'mid', 1n, next(), 'a'))
    applyOp(state, setTabTileOp('t1', 'mid', 1n, next(), 'a'))
    const cache = new ProjectionCache()
    // The cycle stub is path-dependent, so it is deliberately never cached and
    // its ancestors rebuild every call. Correctness is what matters here, and it
    // is the only thing asserted -- identity is explicitly not promised.
    expect(project(state, cache)).toEqual(project(state))
    expect(project(state, cache)).toEqual(project(state))
  })

  it('rebuilds after an entity is removed and re-materialized under the same id', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    project(state, cache)
    // What `consumeEntityRemoved` does: delete the map slot outright, no
    // tombstone. The re-materialized record is a different object, so nothing
    // from the old generation may survive.
    delete state.nodes.leafA1
    delete state.tabs.tabA1
    const gone = project(state, cache)
    expect(gone).toEqual(project(state))
    expect(rowFor(gone, 'tabA1')).toBeUndefined()

    applyOp(state, setNodeKindOp('leafA1', NodeKind.LEAF, 1n, next(), 'c'))
    applyOp(state, setNodeParentOp('leafA1', 'rootA', 1n, next(), 'c'))
    applyOp(state, setNodePosOp('leafA1', 'N', 1n, next(), 'c'))
    applyOp(state, setTabTileOp('tabA1', 'leafA1', 1n, next(), 'c'))
    const back = project(state, cache)
    expect(back).toEqual(project(state))
    expect(rowFor(back, 'tabA1')?.tileId).toBe('leafA1')
  })

  /**
   * A tab whose tile is momentarily unresolvable is HELD on screen by
   * `tabView`, and `mapArray` keys its per-tab computation by row REFERENCE. So
   * the row it comes back with when the tile resolves has to be the one
   * consumers are already holding -- otherwise that computation is disposed and
   * the tab re-assembled for a change that never happened.
   *
   * Eviction is generational, so the entry is dropped on the FIRST unplaceable
   * tick unless it is explicitly carried forward.
   */
  it('keeps a tab row across a tick its tile could not be resolved on', () => {
    const { state } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    const row = rowFor(before, 'tabA1')
    expect(row).toBeDefined()

    // The tile record is not there yet -- the frame that would explain it has
    // not arrived, so no chain reaches a root and the tab drops out of the
    // projection while `tabView` holds it on screen.
    const tile = state.nodes.leafA1
    delete state.nodes.leafA1
    const gone = project(state, cache)
    expect(rowFor(gone, 'tabA1'), 'unplaceable while the tile is unknown').toBeUndefined()
    expect(gone).toEqual(project(state))

    state.nodes.leafA1 = tile
    const back = project(state, cache)
    expect(rowFor(back, 'tabA1'), 'the row survived the gap').toBe(row)
    expect(back).toEqual(project(state))
  })

  /**
   * ...but retaining must not RESURRECT. The entry carries the tab's whole
   * input set, so a register that moved while the tab was unplaceable misses on
   * identity and rebuilds, exactly as it would have one generation later.
   */
  it('does not resurrect a retained row when the tab moved while unresolved', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    const row = rowFor(before, 'tabA1')

    const tile = state.nodes.leafA1
    delete state.nodes.leafA1
    project(state, cache)
    applyOp(state, setTabTileOp('tabA1', 'leafA2', 2n, next(), 'a'))
    state.nodes.leafA1 = tile

    const after = project(state, cache)
    expect(rowFor(after, 'tabA1')?.tileId).toBe('leafA2')
    expect(rowFor(after, 'tabA1')).not.toBe(row)
    expect(after).toEqual(project(state))
  })

  /**
   * A tenant change must not be absorbed by whole-projection reuse.
   *
   * `commit` deliberately does not compare `userId` -- `begin` owns the tenant
   * rule -- so this is the one thing that keeps the two from disagreeing. The
   * state is otherwise UNTOUCHED here, which is what gives the test teeth: every
   * part would reuse and `commit` would hand back the previous `Projection`
   * object, still reporting the old tenant, if the reset had not dropped it.
   */
  it('drops everything when the tenant changes', () => {
    const { state } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    expect(before.userId).toBe('user')
    expect(project(state, cache), 'settled, so reuse is live').toBe(before)

    state.userId = 'other'
    const after = project(state, cache)
    expect(after, 'not the previous tenant\'s projection object').not.toBe(before)
    expect(after.userId).toBe('other')
    expect(after.workspaces.get('wA'), 'nor any part of it').not.toBe(before.workspaces.get('wA'))
    expect(after).toEqual(project(state))
  })

  /**
   * Every register `CachedNode` carries, one per case, asserted to force a
   * rebuild on its own.
   *
   * The entry compares nine fields against the live record, and each comparison
   * is written out by hand -- `prev.rows === rec?.rows`. Every node register has
   * the SAME generated type as its neighbours (`rows`/`cols` are both
   * `LWWUint32 | undefined`; `ratios`/`rowRatios`/`colRatios` are all
   * `LWWDoubles | undefined`), so a copy-paste slip like `prev.rows ===
   * rec?.cols` type-checks and silently disables reuse for that node -- the
   * projection stays CORRECT, so no output test can see it. This walks the
   * table instead: change exactly one register and the subtree must be a new
   * object.
   *
   * Three of the nine cannot be reached this way and are covered in the class
   * doc instead: `tombstoneAt` (tombstoning REPLACES the record, so the node
   * loses every register), `CachedTab.tabType` (set-once on a live record) and
   * `CachedWindow.rootNodeId` (set-once, and `""` projects to a different tree).
   *
   * The table is KEYED by register name and its type is DERIVED from
   * `NodeRegisterKey`, which is what turns it from a list someone must remember
   * to extend into a tripwire: add a register to the proto and this object stops
   * type-checking until a case exists, and that case then fails until
   * `ProjectionCache.tree` actually compares the register. Driving the
   * comparison itself off a key array would give the same guarantee in the
   * source, but `tree` runs it once per node per tick on the HIT path, where a
   * keyed loop measured ~15x the `&&` chain -- more than the whole reuse win. So
   * the guarantee is bought here, where it costs nothing.
   */
  const NODE_REGISTERS: { [K in Exclude<NodeRegisterKey, 'tombstoneAt'>]: (next: () => bigint) => ReturnType<typeof setNodeKindOp> } = {
    kind: next => setNodeKindOp('grid', NodeKind.SPLIT, 1n, next(), 'a'),
    direction: next => setNodeDirOp('grid', SplitDirection.VERTICAL, 1n, next(), 'a'),
    ratios: next => setNodeRatiosOp('grid', [0.9, 0.1], 1n, next(), 'a'),
    rows: next => setNodeRowsOp('grid', 3, 1n, next(), 'a'),
    cols: next => setNodeColsOp('grid', 3, 1n, next(), 'a'),
    rowRatios: next => setNodeOp('grid', { case: 'rowRatios', value: doubles([0.7, 0.3]) }, 1n, next(), 'a'),
    colRatios: next => setNodeOp('grid', { case: 'colRatios', value: doubles([0.7, 0.3]) }, 1n, next(), 'a'),
  }

  it.each(Object.entries(NODE_REGISTERS))('rebuilds the subtree when %s changes', (_name, write) => {
    // A live 2x2 GRID under the workspace root, so every register in the table
    // is one the node actually reads: a LEAF ignores rows/cols/grid ratios and a
    // SPLIT ignores the grid pair, and a comparison that never fires would look
    // like a pass.
    const state = newState('user')
    state.workspaces.w = create(WorkspaceContentsRecordSchema, { workspaceId: 'w', rootNodeId: 'grid' })
    let seq = 0
    const next = () => BigInt(++seq)
    applyOp(state, setNodeKindOp('grid', NodeKind.GRID, 1n, next(), 'a'))
    applyOp(state, setNodeRowsOp('grid', 2, 1n, next(), 'a'))
    applyOp(state, setNodeColsOp('grid', 2, 1n, next(), 'a'))
    applyOp(state, setNodeOp('grid', { case: 'rowRatios', value: doubles([0.5, 0.5]) }, 1n, next(), 'a'))
    applyOp(state, setNodeOp('grid', { case: 'colRatios', value: doubles([0.5, 0.5]) }, 1n, next(), 'a'))
    applyOp(state, setNodeRatiosOp('grid', [0.5, 0.5], 1n, next(), 'a'))
    applyOp(state, setNodeDirOp('grid', SplitDirection.HORIZONTAL, 1n, next(), 'a'))
    for (const [i, cell] of ['0,0', '0,1', '1,0', '1,1'].entries()) {
      applyOp(state, setNodeKindOp(`c${i}`, NodeKind.LEAF, 1n, next(), 'a'))
      applyOp(state, setNodeParentOp(`c${i}`, 'grid', 1n, next(), 'a'))
      applyOp(state, setNodePosOp(`c${i}`, cell, 1n, next(), 'a'))
    }

    const cache = new ProjectionCache()
    const before = project(state, cache)
    expect(project(state, cache), 'settled before the write').toBe(before)

    applyOp(state, write(next))
    const after = project(state, cache)
    const tree = (p: ReturnType<typeof project>) => p.workspaces.get('w')?.mainTree
    expect(tree(after), 'the register is not compared').not.toBe(tree(before))
    expect(after, 'and the answer still matches the uncached projector').toEqual(project(state))
  })

  /**
   * Ordering both tab arrays by id is the one part of `project()` the cache
   * makes arithmetically cheaper rather than just allocation-cheaper: rows are
   * collected in record-creation order and tab ids are nanoids, so the sort is a
   * real O(n log n) of string compares -- over half a settled tick at 300 tabs.
   *
   * Spying on `Array.prototype.sort` is what makes that observable. An identity
   * assertion cannot: the settled tick already returned the identical projection
   * BEFORE this memo existed, because `reuseArray` compares the sorted arrays
   * element-wise and they were equal -- the sort simply ran first and was thrown
   * away.
   */
  it('does not re-sort the tab arrays on a tick where no tab moved', () => {
    // Insertion order c,a,b -- deliberately not the projected order, so the
    // first call's sort does real work.
    const state = seedRoot('w1', 'root')
    let l = 0n
    for (const id of ['t-c', 't-a', 't-b'])
      applyOp(state, setTabTileOp(id, 'root', 2n, l++, 'a'))
    const cache = new ProjectionCache()
    const first = project(state, cache)
    expect(first.renderedTabs.map(t => t.tabId)).toEqual(['t-a', 't-b', 't-c'])

    const original = Array.prototype.sort
    const tabSorts: number[] = []
    const spy = vi.spyOn(Array.prototype, 'sort').mockImplementation(function (this: unknown[], cmp?: never) {
      const head = this[0]
      // `registeredRoots` sorts id strings and `buildTree` sorts NodeRecords;
      // only the two tab arrays hold rows with a `tabId`.
      if (typeof head === 'object' && head !== null && 'tabId' in head)
        tabSorts.push(this.length)
      return original.call(this, cmp)
    } as typeof Array.prototype.sort)
    try {
      expect(project(state, cache), 'settled tick').toBe(first)
    }
    finally {
      spy.mockRestore()
    }
    expect(tabSorts, 'the row set and its order were identical -- so was the answer').toEqual([])
  })

  it('re-sorts when a tab moves, and the memo does not serve the stale order', () => {
    const state = seedRoot('w1', 'root')
    state.workspaces.w2 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w2', rootNodeId: 'root2' })
    let l = 0n
    applyOp(state, setNodeKindOp('root2', NodeKind.LEAF, 2n, l++, 'a'))
    for (const id of ['t-c', 't-a', 't-b'])
      applyOp(state, setTabTileOp(id, 'root', 2n, l++, 'a'))
    const cache = new ProjectionCache()
    const before = project(state, cache)
    expect(before.renderedTabs.map(t => t.tabId)).toEqual(['t-a', 't-b', 't-c'])
    expect(before.renderedTabs.every(t => t.workspaceId === 'w1')).toBe(true)

    // Same rows, same ORDER of collection, different workspace on one of them --
    // so the memo must miss on the row's identity, not just on the id list.
    applyOp(state, setTabTileOp('t-a', 'root2', 3n, l++, 'a'))
    const after = project(state, cache)
    expect(after.renderedTabs.map(t => t.tabId), 'still ordered by id').toEqual(['t-a', 't-b', 't-c'])
    expect(after.renderedTabs.find(t => t.tabId === 't-a')?.workspaceId).toBe('w2')
    expect(after, 'and it matches the uncached projector').toEqual(project(state))
  })

  /**
   * The commit echo, which is what half of every drag's ticks actually are.
   *
   * `submit` writes under the client HLC and the `BatchCommitted` echo re-applies
   * the SAME value under the canonical one. That replaces the register object, so
   * the input comparison says "changed" and the subtree is rebuilt -- byte for
   * byte identical. Comparing the rebuilt object against the previous one is what
   * turns that back into a no-op tick, and a no-op tick is the whole point: no
   * `renderTreeToLocal`, no `sameLayoutNode` walk, no memo downstream of
   * `projection()` re-runs at all.
   */
  it('keeps every object across a same-value register rewrite', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)

    applyOp(state, setNodeRatiosOp('rootA', [0.5, 0.5], 1n, next(), 'a'))
    const after = project(state, cache)
    expect(after, 'the echo propagates nothing').toBe(before)
    expect(after, 'and still answers what the uncached projector does').toEqual(project(state))
  })

  it('still propagates when the rewrite carries a different value', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)

    applyOp(state, setNodeRatiosOp('rootA', [0.3, 0.7], 1n, next(), 'a'))
    const after = project(state, cache)
    expect(after, 'a real drag must not be swallowed').not.toBe(before)
    expect(after.workspaces.get('wA')?.mainTree.ratios).toEqual([0.3, 0.7])
    expect(after.workspaces.get('wB'), 'and the untouched workspace still settles').toBe(before.workspaces.get('wB'))
    expect(after).toEqual(project(state))
  })

  it('keeps a floating window across a same-value geometry rewrite', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    expect(before.workspaces.get('wA')?.floatingWindows[0]?.x).toBe(10)

    // A pointermove that lands on the coordinate the window already has -- the
    // shape a drag's commit echo takes for geometry.
    applyOp(state, setFwXOp('fw1', 10, 1n, next(), 'a'))
    const after = project(state, cache)
    expect(after, 'a geometry echo propagates nothing').toBe(before)
    expect(after).toEqual(project(state))
  })

  it('keeps a tab row across a same-value register rewrite', () => {
    const { state, next } = twoWorkspaces()
    const cache = new ProjectionCache()
    const before = project(state, cache)
    expect(rowFor(before, 'tabA1')?.tileId).toBe('leafA1')

    // `worker_id` re-stamped with what it already holds -- the shape a metadata
    // echo takes. `CachedTab` compares register refs, so this misses on input.
    applyOp(state, setTabOp('tabA1', { case: 'workerId', value: '' }, 1n, next(), 'a'))
    const after = project(state, cache)
    expect(after, 'nothing observable moved').toBe(before)
    expect(after).toEqual(project(state))
  })
})
