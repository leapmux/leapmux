import type {
  FloatingWindowRecord,
  NodeRecord,
  UserCrdtState,
} from '~/generated/leapmux/v1/user_crdt_pb'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { NodeKind } from '~/generated/leapmux/v1/user_crdt_pb'
import { hlcIsZero } from './hlc'

/**
 * The tiling tree the client draws.
 *
 * Client-only: the hub's projection deliberately carries no render tree (see
 * `Projection` in `backend/internal/hub/crdt/project.go`), because layout
 * belongs to whoever draws it and a second unexercised copy of the tiling rules
 * only gave the two implementations somewhere to drift. What the two sides DO
 * share is tab ownership/rendering, pinned by
 * `testdata/crdt_projection_conformance.json`.
 */
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
  userId: string
  workspaceId: string
  tabType: TabType
  tabId: string
  workerId: string
  tileId: string
  position: string
}

export interface RenderedFloatingWindow {
  windowId: string
  /**
   * The window's seed-root node id. Carried so a consumer can name the empty
   * window when `innerTree` resolves to nothing -- the store's fallback leaf
   * needs a stable id, and minting one per tick would remount the tile.
   */
  rootNodeId: string
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
  userId: string
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

export function registeredRoots(state: UserCrdtState): RootSet {
  const roots = new Map<string, string>()
  // Ascending id order, FIRST claim wins -- on both sides.
  //
  // Two workspaces can name the same root node id. The hub's commit path
  // rejects that, but the projection is total and still has to answer for
  // speculative state, and the conformance harnesses apply ops raw with no
  // validator. Relying on `Object.entries` order looks deterministic here but
  // is not a property the two implementations can SHARE: Go's map has no
  // order, and these records arrive over the wire, where protobuf leaves map
  // field ordering unspecified -- so this object's order is decode order, not
  // anything two clients agree on. Ordering on a value IN the data is.
  //
  // `project.go:registeredRoots` sorts and first-wins identically.
  for (const wsId of Object.keys(state.workspaces).sort()) {
    const ws = state.workspaces[wsId]
    if (ws.rootNodeId !== '' && !roots.has(ws.rootNodeId))
      roots.set(ws.rootNodeId, wsId)
  }
  // Windows after workspaces, so a workspace root outranks a window root on
  // collision -- a workspace root is protected even from internal batches,
  // while a window root may be tombstoned by an internal sweep.
  for (const windowId of Object.keys(state.floatingWindows).sort()) {
    const fw = state.floatingWindows[windowId]
    // Only LIVE (non-tombstoned) floating windows contribute a root.
    // Mirrors `backend/internal/hub/crdt/project.go:registeredRoots`,
    // which skips tombstoned windows with the same guard. The prior
    // `!hlcIsZero` typo EXCLUDED live windows from the root set, which
    // meant the projection's `resolveTileWorkspace` could never reach
    // a floating-window root: any tab moved into a floating window
    // dropped out of `renderedTabs`, and `reconcileFromProjection`
    // then deleted it from the local tab store. Popped-out tabs
    // vanished immediately as a result.
    if (hlcIsZero(fw.tombstoneAt) && fw.rootNodeId !== '' && !roots.has(fw.rootNodeId)) {
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
 * in a separate `chainAlive` helper).
 *
 * An intermediate tombstoned ancestor returns `workspaceId: ''`; a
 * tombstoned resolved root returns `workspaceId, alive: false`. Both
 * now drop the tab from BOTH views, because `ownershipHolds` requires
 * a live chain — the two used to differ, which is what let a
 * tombstoned workspace root leave its tabs owned-but-unrendered while
 * any other tombstoned tile dropped them outright.
 *
 * `registeredRoots` indexes workspace roots by id only, so the
 * resolved root's own NodeRecord must be re-checked for a tombstone at
 * chain-end. An ABSENT root record counts as alive: the root is
 * registered, its NodeRecord simply has not materialised yet, and that
 * window is exactly what `tabView`'s hold-in-place exists for.
 */
function resolveTileWorkspace(state: UserCrdtState, tileId: string, roots: RootSet): { workspaceId: string, alive: boolean } {
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

function tileIsLeaf(state: UserCrdtState, tileId: string): boolean {
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
export function buildChildIndex(state: UserCrdtState): Map<string, NodeRecord[]> {
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

function buildTreeFromRoot(state: UserCrdtState, rootId: string, roots: RootSet, childIndex: Map<string, NodeRecord[]>): RenderTree {
  if (rootId === '')
    return { nodeId: '', kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
  const rec = state.nodes[rootId]
  if (!rec || !hlcIsZero(rec.tombstoneAt)) {
    return { nodeId: rootId, kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
  }
  return buildTree(state, rec, roots, childIndex, new Set())
}

function buildTree(state: UserCrdtState, rec: NodeRecord, roots: RootSet, childIndex: Map<string, NodeRecord[]>, seen: Set<string>): RenderTree {
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
      // SPLIT with one live child renders as just that child (visual collapse),
      // under the CHILD's own node id.
      //
      // It used to re-key the child to the SPLIT's id. That gave one rendered
      // surface two identities -- the leaf's real, addressable, writable CRDT id
      // and the ancestor's -- and every consumer had to compensate: the view
      // aliased tab placements back onto the renamed id, `emitCloseTile` walked
      // up the collapse chain to migrate tabs to the topmost ancestor, and a
      // path that forgot to (`emitRemoveGrid`) left tabs alive on a tile the
      // tree no longer advertised, i.e. invisible. Rendering the child as
      // itself removes the second identity and all three compensations with it.
      if (sorted.length === 1)
        return buildTree(state, sorted[0], roots, childIndex, seen)
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

/**
 * Is a tab whose tile resolved to `workspaceId` the user's tab at all?
 *
 * `renderedTabs` then narrows `ownedTabs` by leaf-ness alone, which makes it a
 * strict subset by construction. Two conditions, both of which used to be
 * applied unevenly:
 *
 *   - The CHAIN must be alive. `resolveTileWorkspace` consults the roots map
 *     before the tombstone check, so reaching a registered root short-circuits
 *     the walk even when that root node is tombstoned. Liveness therefore only
 *     gated rendering, and a tab on a tombstoned ROOT stayed owned while a tab
 *     on any other tombstoned tile dropped from both views. Same dead tile, two
 *     different answers.
 *
 *   - The WORKSPACE must still exist. A floating window carries its own
 *     `workspace_id` and registers its root from the window record, so deleting
 *     the `WorkspaceContentsRecord` left the window's tabs resolving to a
 *     workspace `project()` no longer contains — the window is dropped from the
 *     tree below, so those tabs claimed to be on screen with nothing to draw
 *     them in.
 *
 * Checked here rather than in `registeredRoots` so the hub can mirror it: its
 * root map is shared with batch validation and subscriber broadcast filtering,
 * and narrowing it there would change both.
 */
function ownershipHolds(state: UserCrdtState, workspaceId: string, chainAlive: boolean): boolean {
  return workspaceId !== '' && chainAlive && state.workspaces[workspaceId] !== undefined
}

/**
 * Stable ascending string compare, by code point. Used to sort by
 * id/tabId/windowId — and by `tabView`, whose visible order must agree with
 * this one. Deliberately not `localeCompare`: Intl collation reorders both
 * LexoRank positions (under lt/lv) and mixed-case nanoid ids (under every
 * locale).
 */
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
 *
 * PERF: this rebuilds EVERY workspace's tree and walks the whole tab map on
 * every call, and its caller is a memo over an `{ equals: false }` signal that
 * fires per CRDT op (~60/s during a drag, per the comment below). The parent
 * commit had workspace-scoped projectors on that path; they were deleted here.
 * The hub already solves the same shape incrementally in
 * `backend/internal/hub/crdt/tabindex.go` (`DiffProjectionForBatch`).
 * Tracked: https://github.com/leapmux/leapmux/issues/336
 */
export function project(state: UserCrdtState): Projection {
  const out: Projection = {
    userId: state.userId,
    workspaces: new Map(),
    ownedTabs: [],
    renderedTabs: [],
  }
  const roots = registeredRoots(state)
  const childIndex = buildChildIndex(state)
  // Memoize tile -> (workspaceId, alive) so multi-tab leaves don't re-walk
  // identical parent chains. `state` and `roots` are fixed for the duration of
  // this call and `resolveTileWorkspace` is pure, so the memo is exact.
  //
  // Mirrors the hub's `tileMemo` in `backend/internal/hub/crdt/project.go`
  // (`projectTabs`), which added it for the same reason -- except the client
  // needs it MORE: `project()` re-runs on every CRDT tick, ~60/s while a tile
  // is dragged, where the hub runs it once per commit. `tileIsLeaf` stays
  // outside the memo: it is a single map lookup, and the Go twin also calls it
  // per tab.
  const tileMemo = new Map<string, { workspaceId: string, alive: boolean }>()
  const resolveTile = (tileId: string): { workspaceId: string, alive: boolean } => {
    let res = tileMemo.get(tileId)
    if (!res) {
      res = resolveTileWorkspace(state, tileId, roots)
      tileMemo.set(tileId, res)
    }
    return res
  }
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
    const { workspaceId, alive } = resolveTile(tile)
    if (!ownershipHolds(state, workspaceId, alive))
      continue
    const row: RenderedTab = {
      userId: state.userId,
      workspaceId,
      tabType: t.tabType,
      tabId: t.tabId,
      workerId: t.workerId?.value ?? '',
      tileId: tile,
      position: t.position?.value ?? '',
    }
    // Both views report the same `tile_id`, because the rendered tree now
    // advertises every leaf under its own node id -- see the SPLIT collapse in
    // `buildTree`. `renderedTabs` narrows `ownedTabs` to the tabs that are on a
    // live leaf; it does not relabel them.
    out.ownedTabs.push(row)
    if (tileIsLeaf(state, tile))
      out.renderedTabs.push(row)
  }
  out.ownedTabs.sort((a, b) => cmpStr(a.tabId, b.tabId))
  out.renderedTabs.sort((a, b) => cmpStr(a.tabId, b.tabId))

  return out
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
  state: UserCrdtState,
  fw: FloatingWindowRecord,
  precomputed?: { roots: RootSet, childIndex: Map<string, NodeRecord[]> },
): RenderedFloatingWindow | undefined {
  if (!hlcIsZero(fw.tombstoneAt))
    return undefined
  const roots = precomputed?.roots ?? registeredRoots(state)
  const childIndex = precomputed?.childIndex ?? buildChildIndex(state)
  return {
    windowId: fw.windowId,
    rootNodeId: fw.rootNodeId,
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
// tombstoned. It DELEGATES the window shape to `floatingWindowToRendered`
// rather than repeating it: both are live (the latter backs
// `floatingWindow.store`), so a second copy of the field list is a register
// that lands in one path and silently not the other.
function projectFloatingWindow(state: UserCrdtState, fw: FloatingWindowRecord, roots: RootSet, childIndex: Map<string, NodeRecord[]>): { workspaceId: string, window: RenderedFloatingWindow } | null {
  const window = floatingWindowToRendered(state, fw, { roots, childIndex })
  if (!window)
    return null
  return { workspaceId: fw.workspaceId?.value ?? '', window }
}

/**
 * Return the parent node_id of `nodeId` in `state`, or "" if the node
 * is a root or absent. Mirrors the CRDT model: parent_id is set-once
 * at creation, so the answer is stable for any live node.
 */
export function parentOf(state: UserCrdtState, nodeId: string): string {
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
  state: UserCrdtState,
  nodeId: string,
  childIndex?: Map<string, NodeRecord[]>,
): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const idx = childIndex ?? buildChildIndex(state)
  visit(state, nodeId, idx, seen, out)
  return out
}

function visit(state: UserCrdtState, nodeId: string, childIndex: Map<string, NodeRecord[]>, seen: Set<string>, out: string[]): void {
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
