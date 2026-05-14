import type {
  FloatingWindowRecord,
  NodeRecord,
  OrgCrdtState,
} from '~/generated/leapmux/v1/org_crdt_pb'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import { hlcIsZero } from './hlc'

/** RenderTree mirrors the Go-side projection shape. */
export interface RenderTree {
  nodeId: string
  kind: NodeKind
  direction: number
  ratios: number[]
  rows: number
  cols: number
  rowRatios: number[]
  colRatios: number[]
  children: RenderTree[]
}

export interface RenderedTab {
  orgId: string
  workspaceId: string
  tabType: TabType
  tabId: string
  workerId: string
  tileId: string
  position: string
}

export interface RenderedFloatingWindow {
  windowId: string
  x: number
  y: number
  width: number
  height: number
  opacity: number
  innerTree: RenderTree
}

export interface WorkspaceProjection {
  workspaceId: string
  mainTree: RenderTree
  floatingWindows: RenderedFloatingWindow[]
}

export interface Projection {
  orgId: string
  workspaces: Map<string, WorkspaceProjection>
  ownedTabs: RenderedTab[]
  renderedTabs: RenderedTab[]
}

/**
 * RootSet maps every registered root node_id (workspace + floating
 * window roots) to its owning workspace_id. Exported so callers that
 * render multiple subtrees from the same state can precompute it once
 * and pass it via `floatingWindowToRendered`'s `precomputed` arg.
 */
export interface RootSet {
  roots: Map<string, string>
}

export function registeredRoots(state: OrgCrdtState): RootSet {
  const roots = new Map<string, string>()
  for (const [wsId, ws] of Object.entries(state.workspaces)) {
    if (ws.rootNodeId !== '')
      roots.set(ws.rootNodeId, wsId)
  }
  for (const fw of Object.values(state.floatingWindows)) {
    // Only LIVE (non-tombstoned) floating windows contribute a root.
    // Mirrors `backend/internal/hub/crdt/project.go:registeredRoots`,
    // which skips tombstoned windows with the same guard. The prior
    // `!hlcIsZero` typo EXCLUDED live windows from the root set, which
    // meant the projection's `resolveTileWorkspace` could never reach
    // a floating-window root: any tab moved into a floating window
    // dropped out of `renderedTabs`, and `reconcileFromProjection`
    // then deleted it from the local tab store. Popped-out tabs
    // vanished immediately as a result.
    if (hlcIsZero(fw.tombstoneAt) && fw.rootNodeId !== '') {
      roots.set(fw.rootNodeId, fw.workspaceId?.value ?? '')
    }
  }
  return { roots }
}

/**
 * Walk parent_id chain to a registered root; return its workspace_id
 * and whether the chain is fully alive. A single walk covers both
 * cycle detection (`visited`) and tombstone-along-the-chain checking
 * — the previous shape walked the same chain twice (once here, once
 * in a separate `chainAlive` helper). Semantics are preserved
 * exactly: an intermediate tombstoned ancestor returns
 * `workspaceId: ''` (the tab is dropped entirely), while a tombstoned
 * resolved root returns `workspaceId, alive: false` (the tab is owned
 * but not rendered). `registeredRoots` indexes workspace roots by id
 * only, so the resolved root's own NodeRecord must be re-checked for
 * a tombstone at chain-end.
 */
function resolveTileWorkspace(state: OrgCrdtState, tileId: string, roots: RootSet): { workspaceId: string, alive: boolean } {
  if (tileId === '')
    return { workspaceId: '', alive: false }
  const visited = new Set<string>()
  let cur = tileId
  for (;;) {
    if (visited.has(cur))
      return { workspaceId: '', alive: false }
    visited.add(cur)
    const wsId = roots.roots.get(cur)
    if (wsId !== undefined) {
      // Root reached. The chain up to here is alive (we'd have early-
      // returned otherwise). Workspace roots are registered without
      // checking their NodeRecord's `tombstoneAt`, so re-read the
      // root node and flag the chain dead if it was tombstoned.
      const rootNode = state.nodes[cur]
      const alive = !rootNode || hlcIsZero(rootNode.tombstoneAt)
      return { workspaceId: wsId, alive }
    }
    const node = state.nodes[cur]
    if (!node || !hlcIsZero(node.tombstoneAt))
      return { workspaceId: '', alive: false }
    if (node.parentId === '')
      return { workspaceId: '', alive: false }
    cur = node.parentId
  }
}

function tileIsLeaf(state: OrgCrdtState, tileId: string): boolean {
  const rec = state.nodes[tileId]
  if (!rec)
    return false
  return rec.kind?.value === NodeKind.LEAF
}

/**
 * Build a parent_id → live children index in a single O(N) pass so
 * the recursive tree walk does O(fanout) per node instead of O(N).
 * Exported so callers rendering many subtrees from the same state can
 * compute the index once and feed it to
 * `floatingWindowToRendered`'s `precomputed` arg.
 */
export function buildChildIndex(state: OrgCrdtState): Map<string, NodeRecord[]> {
  const idx = new Map<string, NodeRecord[]>()
  for (const n of Object.values(state.nodes)) {
    if (!hlcIsZero(n.tombstoneAt))
      continue
    const arr = idx.get(n.parentId)
    if (arr)
      arr.push(n)
    else idx.set(n.parentId, [n])
  }
  return idx
}

function buildTreeFromRoot(state: OrgCrdtState, rootId: string, roots: RootSet, childIndex: Map<string, NodeRecord[]>): RenderTree {
  if (rootId === '')
    return { nodeId: '', kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
  const rec = state.nodes[rootId]
  if (!rec || !hlcIsZero(rec.tombstoneAt)) {
    return { nodeId: rootId, kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
  }
  return buildTree(state, rec, roots, childIndex, new Set())
}

function buildTree(state: OrgCrdtState, rec: NodeRecord, roots: RootSet, childIndex: Map<string, NodeRecord[]>, seen: Set<string>): RenderTree {
  if (seen.has(rec.nodeId)) {
    return { nodeId: rec.nodeId, kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
  }
  seen.add(rec.nodeId)
  const tree: RenderTree = {
    nodeId: rec.nodeId,
    kind: rec.kind?.value ?? NodeKind.LEAF,
    direction: rec.direction?.value ?? 0,
    ratios: [...(rec.ratios?.value?.values ?? [])],
    rows: rec.rows?.value ?? 0,
    cols: rec.cols?.value ?? 0,
    rowRatios: [...(rec.rowRatios?.value?.values ?? [])],
    colRatios: [...(rec.colRatios?.value?.values ?? [])],
    children: [],
  }
  const children = childIndex.get(rec.nodeId) ?? []
  switch (tree.kind) {
    case NodeKind.SPLIT: {
      const sorted = children.slice().sort((a, b) => {
        const pa = a.position?.value ?? ''
        const pb = b.position?.value ?? ''
        return pa !== pb ? cmpStr(pa, pb) : cmpStr(a.nodeId, b.nodeId)
      })
      // SPLIT with one live child renders as just that child (visual collapse).
      if (sorted.length === 1) {
        const only = buildTree(state, sorted[0], roots, childIndex, seen)
        only.nodeId = tree.nodeId
        return only
      }
      for (const c of sorted) tree.children.push(buildTree(state, c, roots, childIndex, seen))
      tree.ratios = normalizeRatios(tree.ratios, tree.children.length)
      break
    }
    case NodeKind.GRID: {
      if (tree.rows === 0 || tree.cols === 0)
        break
      if (tree.rowRatios.length !== tree.rows)
        tree.rowRatios = normalizeRatios(tree.rowRatios, tree.rows)
      if (tree.colRatios.length !== tree.cols)
        tree.colRatios = normalizeRatios(tree.colRatios, tree.cols)
      const grid = new Map<string, NodeRecord>()
      for (const c of children) {
        const pos = c.position?.value ?? ''
        const existing = grid.get(pos)
        if (!existing || c.nodeId < existing.nodeId)
          grid.set(pos, c)
      }
      tree.children = []
      for (let r = 0; r < tree.rows; r++) {
        for (let col = 0; col < tree.cols; col++) {
          const key = `${r},${col}`
          const entry = grid.get(key)
          if (entry)
            tree.children.push(buildTree(state, entry, roots, childIndex, seen))
          else tree.children.push({ nodeId: '', kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] })
        }
      }
      break
    }
  }
  return tree
}

/** Stable ascending string compare. Used to sort by id/tabId/windowId. */
export function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0
}

function normalizeRatios(ratios: number[], n: number): number[] {
  if (n <= 0)
    return []
  const out = Array.from<number>({ length: n })
  for (let i = 0; i < n; i++) out[i] = 1.0 / n
  for (let i = 0; i < n && i < ratios.length; i++) {
    if (ratios[i] >= 0)
      out[i] = ratios[i]
  }
  return out
}

/**
 * Project applies the deterministic repair rules and returns the
 * renderable projection. Rules: tombstoned skipped, orphans dropped,
 * cycles broken, single-child SPLIT rendered as the child, duplicate
 * grid cells tie-broken by lower node_id, missing grid cells render
 * as virtual empty leaves, bad ratio lengths normalized.
 */
export function project(state: OrgCrdtState): Projection {
  const out: Projection = {
    orgId: state.orgId,
    workspaces: new Map(),
    ownedTabs: [],
    renderedTabs: [],
  }
  const roots = registeredRoots(state)
  const childIndex = buildChildIndex(state)

  for (const [wsId, ws] of Object.entries(state.workspaces)) {
    out.workspaces.set(wsId, {
      workspaceId: wsId,
      mainTree: buildTreeFromRoot(state, ws.rootNodeId, roots, childIndex),
      floatingWindows: [],
    })
  }
  for (const fw of Object.values(state.floatingWindows)) {
    const projected = projectFloatingWindow(state, fw, roots, childIndex)
    if (!projected)
      continue
    const ws = out.workspaces.get(projected.workspaceId)
    if (!ws)
      continue
    ws.floatingWindows.push(projected.window)
  }
  for (const ws of out.workspaces.values()) {
    ws.floatingWindows.sort((a, b) => cmpStr(a.windowId, b.windowId))
  }

  for (const t of Object.values(state.tabs)) {
    if (!hlcIsZero(t.tombstoneAt))
      continue
    const tile = t.tileId?.value ?? ''
    const { workspaceId, alive } = resolveTileWorkspace(state, tile, roots)
    if (workspaceId === '')
      continue
    const row: RenderedTab = {
      orgId: state.orgId,
      workspaceId,
      tabType: t.tabType,
      tabId: t.tabId,
      workerId: t.workerId?.value ?? '',
      tileId: tile,
      position: t.position?.value ?? '',
    }
    out.ownedTabs.push(row)
    if (alive && tileIsLeaf(state, tile))
      out.renderedTabs.push(row)
  }
  out.ownedTabs.sort((a, b) => cmpStr(a.tabId, b.tabId))
  out.renderedTabs.sort((a, b) => cmpStr(a.tabId, b.tabId))

  return out
}

/**
 * projectWorkspace returns the projection slice for a single
 * workspace. Compared to `project(state).workspaces.get(wsId)` it
 * skips every other workspace's tree build and the org-wide tab
 * projection — so a memo that only needs `ws.mainTree` for the active
 * workspace doesn't pay the org-wide cost on every bridge tick.
 *
 * Floating windows owned by `workspaceId` are included so this can
 * back any consumer that wants the full workspace shape; callers
 * (e.g. `floatingWindow.store`) that have their own per-window
 * projector should keep using that path.
 */
/**
 * projectWorkspaceTabs returns only the renderedTabs for `workspaceId`.
 * Compared to `project(state).renderedTabs.filter(t => t.workspaceId
 * === wsId)`, it skips every other workspace's tree build and avoids
 * walking floating-window subtrees that don't belong to the target
 * workspace. Tabs whose tile chain doesn't terminate at one of the
 * workspace's roots (own mainTree root or any of its floating-window
 * roots) are skipped at the resolve step rather than being projected
 * and filtered after.
 *
 * Used by the AppShell reconciler effect during drag/resize, where
 * pendingVersion bumps every frame and the org-wide `project` cost is
 * the hot path.
 */
export function projectWorkspaceTabs(state: OrgCrdtState, workspaceId: string): RenderedTab[] {
  // Build a workspace-scoped RootSet: just the target workspace's root
  // and any live floating-window roots that belong to it. Tabs whose
  // chain terminates at any other root resolve to workspaceId='' and
  // are skipped.
  const scopedRoots = new Map<string, string>()
  const ws = state.workspaces[workspaceId]
  if (ws?.rootNodeId)
    scopedRoots.set(ws.rootNodeId, workspaceId)
  for (const fw of Object.values(state.floatingWindows)) {
    if (!hlcIsZero(fw.tombstoneAt))
      continue
    if ((fw.workspaceId?.value ?? '') !== workspaceId)
      continue
    if (fw.rootNodeId !== '')
      scopedRoots.set(fw.rootNodeId, workspaceId)
  }
  if (scopedRoots.size === 0)
    return []
  const roots: RootSet = { roots: scopedRoots }

  const out: RenderedTab[] = []
  for (const t of Object.values(state.tabs)) {
    if (!hlcIsZero(t.tombstoneAt))
      continue
    const tile = t.tileId?.value ?? ''
    const { workspaceId: resolvedWs, alive } = resolveTileWorkspace(state, tile, roots)
    if (resolvedWs !== workspaceId)
      continue
    if (!alive || !tileIsLeaf(state, tile))
      continue
    out.push({
      orgId: state.orgId,
      workspaceId: resolvedWs,
      tabType: t.tabType,
      tabId: t.tabId,
      workerId: t.workerId?.value ?? '',
      tileId: tile,
      position: t.position?.value ?? '',
    })
  }
  out.sort((a, b) => cmpStr(a.tabId, b.tabId))
  return out
}

export function projectWorkspace(state: OrgCrdtState, workspaceId: string): WorkspaceProjection | undefined {
  const ws = state.workspaces[workspaceId]
  if (!ws)
    return undefined
  const roots = registeredRoots(state)
  const childIndex = buildChildIndex(state)
  const projection: WorkspaceProjection = {
    workspaceId,
    mainTree: buildTreeFromRoot(state, ws.rootNodeId, roots, childIndex),
    floatingWindows: [],
  }
  for (const fw of Object.values(state.floatingWindows)) {
    const projected = projectFloatingWindow(state, fw, roots, childIndex)
    if (!projected || projected.workspaceId !== workspaceId)
      continue
    projection.floatingWindows.push(projected.window)
  }
  projection.floatingWindows.sort((a, b) => cmpStr(a.windowId, b.windowId))
  return projection
}

/**
 * Helper for floating-window projection. When the caller is rendering
 * multiple floating windows from the same state — e.g. the
 * floatingWindow.store memo — it should precompute the shared
 * `roots` and `childIndex` once and pass them in. Without the
 * precomputed args we build them per-window, which is O(N) per window
 * over the full state.
 */
export function floatingWindowToRendered(
  state: OrgCrdtState,
  fw: FloatingWindowRecord,
  precomputed?: { roots: RootSet, childIndex: Map<string, NodeRecord[]> },
): RenderedFloatingWindow | undefined {
  if (!hlcIsZero(fw.tombstoneAt))
    return undefined
  const roots = precomputed?.roots ?? registeredRoots(state)
  const childIndex = precomputed?.childIndex ?? buildChildIndex(state)
  return {
    windowId: fw.windowId,
    x: fw.x?.value ?? 0,
    y: fw.y?.value ?? 0,
    width: fw.width?.value ?? 0,
    height: fw.height?.value ?? 0,
    opacity: fw.opacity?.value ?? 0,
    innerTree: buildTreeFromRoot(state, fw.rootNodeId, roots, childIndex),
  }
}

// projectFloatingWindow returns the RenderedFloatingWindow shape for
// `fw` plus its owning workspace_id, or null when the window is
// tombstoned. Folded out of project()/projectWorkspace() so the field
// list is in one place — adding a register goes to the helper, not 3
// call sites.
function projectFloatingWindow(state: OrgCrdtState, fw: FloatingWindowRecord, roots: RootSet, childIndex: Map<string, NodeRecord[]>): { workspaceId: string, window: RenderedFloatingWindow } | null {
  if (!hlcIsZero(fw.tombstoneAt))
    return null
  return {
    workspaceId: fw.workspaceId?.value ?? '',
    window: {
      windowId: fw.windowId,
      x: fw.x?.value ?? 0,
      y: fw.y?.value ?? 0,
      width: fw.width?.value ?? 0,
      height: fw.height?.value ?? 0,
      opacity: fw.opacity?.value ?? 0,
      innerTree: buildTreeFromRoot(state, fw.rootNodeId, roots, childIndex),
    },
  }
}

/**
 * Return the parent node_id of `nodeId` in `state`, or "" if the node
 * is a root or absent. Mirrors the CRDT model: parent_id is set-once
 * at creation, so the answer is stable for any live node.
 */
export function parentOf(state: OrgCrdtState, nodeId: string): string {
  return state.nodes[nodeId]?.parentId ?? ''
}

/**
 * Enumerate every descendant of `nodeId` in `state` (including the
 * node itself), in leaves-first order. Used by close-grid / remove-
 * subtree paths to produce a tombstone batch where leaves are
 * tombstoned before their ancestors (the CRDT doesn't require this
 * order, but it keeps the validator's intermediate states clean).
 *
 * Tombstoned nodes are skipped. Cross-node cycles are broken on
 * `seen` membership.
 */
export function descendantsLeavesFirst(
  state: OrgCrdtState,
  nodeId: string,
  childIndex?: Map<string, NodeRecord[]>,
): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const idx = childIndex ?? buildChildIndex(state)
  visit(state, nodeId, idx, seen, out)
  return out
}

function visit(state: OrgCrdtState, nodeId: string, childIndex: Map<string, NodeRecord[]>, seen: Set<string>, out: string[]): void {
  if (seen.has(nodeId))
    return
  seen.add(nodeId)
  const rec = state.nodes[nodeId]
  if (!rec || !hlcIsZero(rec.tombstoneAt))
    return
  for (const n of childIndex.get(nodeId) ?? []) {
    visit(state, n.nodeId, childIndex, seen, out)
  }
  out.push(nodeId)
}
