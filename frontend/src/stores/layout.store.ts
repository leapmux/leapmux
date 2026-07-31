import type { LayoutOwner } from './layoutOwner'
import type { Projection } from '~/lib/crdt'
import { createEffect, createMemo, createSignal } from 'solid-js'
import { renderTreeToLocal, sharedTrees, withBridge } from '~/lib/crdt'
import { makeIdGenerator } from '~/lib/idGenerator'
import { sameKeys } from '~/lib/sameKeys'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import {
  emitCloseTile,
  emitMakeGrid,
  emitRemoveGrid,
  emitReplaceGridWithLeaf,
  emitSplitTile,
  emitUpdateGridRatios,
  emitUpdateRatios,
} from './layoutOps'

// --- Local types (the shape every renderer + tile predicate uses) ---
//
// These types describe the projected, rendered tree shape. The
// canonical state is `UserCrdtState`; `LayoutNodeLocal` is what the
// projection (`~/lib/crdt/project + renderTreeToLocal`) emits for
// downstream UI consumption. There is no longer any local-tree
// mutator — every layout change emits CRDT op batches via the bridge,
// the hub re-broadcasts canonical-HLC-tagged ops, the local
// PendingOpsManager folds them into speculativeState, and the
// projection re-renders.

export type SplitOrientation = 'horizontal' | 'vertical'

export interface SplitNode {
  type: 'split'
  id: string
  direction: SplitOrientation
  ratios: number[]
  children: LayoutNodeLocal[]
}

export interface LeafNode {
  type: 'leaf'
  id: string
}

export interface GridNode {
  type: 'grid'
  id: string
  rows: number
  cols: number
  rowRatios: number[]
  colRatios: number[]
  cells: LayoutNodeLocal[]
}

export type GridAxis = 'row' | 'col'

export type LayoutNodeLocal = SplitNode | LeafNode | GridNode

/**
 * Structural equality for a projected tree.
 *
 * The BACKSTOP, not the mechanism. Identity normally survives a tick already:
 * `project()`'s `ProjectionCache` reuses every subtree the tick left alone --
 * including one rebuilt byte-identical by a same-value register rewrite, which
 * it value-compares and discards -- and `renderTreeToLocal` memoizes the
 * conversion, so an untouched workspace arrives here as the same object and this
 * returns on its first line. What is left for it to catch is the uncached
 * `project(state)` the unit tests call.
 *
 * Without either layer a memo over the projection re-propagates on EVERY CRDT
 * tick, including the per-frame geometry ops a floating-window drag emits and
 * every remote op in a workspace the user is not even looking at. That churn
 * defeats two optimisations that document themselves as bounded:
 * `useFocusInvariant`'s `on([focusedTileId, root])` and `TileRenderer`'s
 * per-root predicate memos.
 *
 * Compares only what consumers actually read, which is the whole node shape --
 * ids, kinds, ratios and child order. A genuine ratio drag still invalidates,
 * which is correct; a tick that leaves the tree byte-identical no longer does.
 */
export function sameLayoutNode(a: LayoutNodeLocal, b: LayoutNodeLocal): boolean {
  if (a === b)
    return true
  if (a.type !== b.type || a.id !== b.id)
    return false
  if (a.type === 'leaf')
    return true
  if (a.type === 'split') {
    const other = b as SplitNode
    if (a.direction !== other.direction || !shallowEqualArrays(a.ratios, other.ratios))
      return false
    return sameChildLists(a.children, other.children)
  }
  const other = b as GridNode
  if (a.rows !== other.rows || a.cols !== other.cols)
    return false
  if (!shallowEqualArrays(a.rowRatios, other.rowRatios) || !shallowEqualArrays(a.colRatios, other.colRatios))
    return false
  return sameChildLists(a.cells, other.cells)
}

function sameChildLists(a: readonly LayoutNodeLocal[], b: readonly LayoutNodeLocal[]): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    if (!sameLayoutNode(a[i], b[i]))
      return false
  }
  return true
}

/**
 * Walk a node's structural children. Splits have `children`, grids
 * have `cells`; both are walked the same way for membership / heir /
 * leaf-walk traversals. Leaves return `[]` so callers can iterate
 * uniformly without a dedicated branch. Internal helper exposed as a
 * private utility shared across the surviving traversal helpers.
 */
function childrenOf(node: LayoutNodeLocal): LayoutNodeLocal[] {
  if (node.type === 'leaf')
    return []
  return node.type === 'grid' ? node.cells : node.children
}

export interface LayoutStoreState {
  root: LayoutNodeLocal
  focusedTileId: string | null
}

export const MAX_GRID_DIMENSION = 20
export const MAX_DEPTH = 3

/**
 * Floor for any single entry in a split's `ratios` array or a grid's
 * `rowRatios`/`colRatios`. Resize handles clamp at this so a pane
 * can't be dragged past invisibility.
 */
export const MIN_SPLIT_RATIO = 0.05

// --- Pure traversal utilities ---

export function getAllTileIds(node: LayoutNodeLocal): string[] {
  if (node.type === 'leaf')
    return [node.id]
  return childrenOf(node).flatMap(getAllTileIds)
}

/**
 * Membership test that early-returns instead of materialising the
 * full leaf id array.
 */
export function containsTileId(node: LayoutNodeLocal, tileId: string): boolean {
  if (node.type === 'leaf')
    return node.id === tileId
  for (const child of childrenOf(node)) {
    if (containsTileId(child, tileId))
      return true
  }
  return false
}

/**
 * True iff the tree has at least two leaves. Walks until the second
 * leaf is found and stops, instead of materialising the full id
 * array via `getAllTileIds(node).length > 1`.
 */
export function hasMultipleLeaves(node: LayoutNodeLocal): boolean {
  let count = 0
  function walk(n: LayoutNodeLocal): boolean {
    if (n.type === 'leaf') {
      count++
      return count >= 2
    }
    for (const c of childrenOf(n)) {
      if (walk(c))
        return true
    }
    return false
  }
  return walk(node)
}

/**
 * Pick the leaf tile that should inherit `closingTileId`'s tabs when
 * the tile is closed. Walks up from the closing leaf to its first
 * ancestor with a sibling subtree, then returns the first leaf in
 * that adjacent sibling (preferring the left/upper neighbor, falling
 * back to the right/lower).
 */
export function findHeirTileId(root: LayoutNodeLocal, closingTileId: string): string | null {
  const path: LayoutNodeLocal[] = []
  if (!buildPathToLeaf(root, closingTileId, path))
    return null
  for (let i = path.length - 2; i >= 0; i--) {
    const parent = path[i]
    const child = path[i + 1]
    const siblings = childrenOf(parent)
    const idx = siblings.findIndex(c => c.id === child.id)
    if (idx < 0)
      continue
    const adj = siblings[idx - 1] ?? siblings[idx + 1]
    if (adj)
      return firstLeafId(adj) ?? null
  }
  return null
}

function buildPathToLeaf(node: LayoutNodeLocal, targetId: string, out: LayoutNodeLocal[]): boolean {
  out.push(node)
  if (node.type === 'leaf') {
    if (node.id === targetId)
      return true
    out.pop()
    return false
  }
  for (const child of childrenOf(node)) {
    if (buildPathToLeaf(child, targetId, out))
      return true
  }
  out.pop()
  return false
}

/**
 * Walk the leftmost descent and return the first leaf id. Returns
 * undefined only if the tree is malformed (a split or grid with no
 * children).
 */
export function firstLeafId(node: LayoutNodeLocal): string | undefined {
  if (node.type === 'leaf')
    return node.id
  const first = childrenOf(node)[0]
  return first ? firstLeafId(first) : undefined
}

/**
 * Locate a node by type + id. Returns null if absent. Used by
 * `findGridById`.
 */
function findNodeByTypeAndId<T extends LayoutNodeLocal['type']>(
  root: LayoutNodeLocal,
  type: T,
  id: string,
): Extract<LayoutNodeLocal, { type: T }> | null {
  if (root.type === type && root.id === id)
    return root as Extract<LayoutNodeLocal, { type: T }>
  for (const child of childrenOf(root)) {
    const found = findNodeByTypeAndId(child, type, id)
    if (found)
      return found
  }
  return null
}

export function findGridById(root: LayoutNodeLocal, gridId: string): GridNode | null {
  return findNodeByTypeAndId(root, 'grid', gridId)
}

// --- Close-affordance + tile-predicate helpers (used by the renderer) ---

export type TileCloseMode
  = | { kind: 'none' }
    | { kind: 'tile' }
    | { kind: 'grid', gridId: string }

export const CLOSE_MODE_NONE: TileCloseMode = { kind: 'none' }
export const CLOSE_MODE_TILE: TileCloseMode = { kind: 'tile' }

export function closeAffordance(
  mode: TileCloseMode,
  surface: 'button' | 'menu',
): { label: string, testId: string } {
  const isGrid = mode.kind === 'grid'
  const label = isGrid ? 'Close grid' : 'Close tile'
  const base = isGrid ? 'close-grid' : 'close-tile'
  const testId = surface === 'menu' ? `${base}-menu-item` : base
  return { label, testId }
}

export interface TilePredicates {
  closeMode: TileCloseMode
  canSplit: boolean
  canMakeGrid: boolean
}

export type LayoutRootKind = 'main' | 'floating'

export function buildTilePredicateMap(
  root: LayoutNodeLocal,
  kind: LayoutRootKind,
): Map<string, TilePredicates> {
  const ctx: PredicateWalkCtx = {
    map: new Map<string, TilePredicates>(),
    kind,
    multiTile: hasMultipleLeaves(root),
  }
  walkPredicates(ctx, root, /* depth */ 0, /* innermostAnchorGridId */ null, /* isDirectGridCell */ false)
  return ctx.map
}

interface PredicateWalkCtx {
  map: Map<string, TilePredicates>
  kind: LayoutRootKind
  multiTile: boolean
}

function walkPredicates(
  ctx: PredicateWalkCtx,
  node: LayoutNodeLocal,
  depth: number,
  innermostAnchorGridId: string | null,
  isDirectGridCell: boolean,
): void {
  if (node.type === 'leaf') {
    let closeMode: TileCloseMode = CLOSE_MODE_NONE
    if (innermostAnchorGridId !== null)
      closeMode = { kind: 'grid', gridId: innermostAnchorGridId }
    else if (!isDirectGridCell && ctx.multiTile)
      closeMode = CLOSE_MODE_TILE
    const withinDepth = ctx.kind === 'floating' || depth < MAX_DEPTH
    ctx.map.set(node.id, {
      closeMode,
      canSplit: withinDepth,
      canMakeGrid: withinDepth,
    })
    return
  }
  if (node.type === 'grid') {
    const anchorIdx = node.cols - 1
    for (let i = 0; i < node.cells.length; i++) {
      const cell = node.cells[i]
      const isAnchorCell = i === anchorIdx
      walkPredicates(
        ctx,
        cell,
        depth + 1,
        isAnchorCell ? node.id : null,
        cell.type === 'leaf',
      )
    }
    return
  }
  const lastIdx = node.children.length - 1
  // Anchor on the top-right visual position. For a vertical-divider
  // split (side-by-side panes) that's the rightmost child; for a
  // horizontal-divider split (stacked panes) that's the topmost.
  const anchorChildIdx = node.direction === 'vertical' ? lastIdx : 0
  for (let i = 0; i < node.children.length; i++) {
    const child = node.children[i]
    walkPredicates(
      ctx,
      child,
      depth + 1,
      i === anchorChildIdx ? innermostAnchorGridId : null,
      false,
    )
  }
}

// --- Close-tile result shape (consumed by floatingWindow.store) ---

export type CloseTileResult
  = | { kind: 'noop' }
    | { kind: 'changed' }
    | { kind: 'disposed', tileIds: ReadonlySet<string> }

// --- Store ---

/**
 * createLayoutStore — projection-driven layout store. The local
 * `state.root` is a memoized derivation of the CRDT projection
 * (`project(bridge.speculativeState())[bridge.workspaceId()].mainTree`).
 * Mutators emit op batches via the bridge; the hub re-broadcasts
 * canonical-HLC-tagged ops, the local PendingOpsManager folds them
 * into speculativeState, and `state.root` re-derives reactively.
 *
 * `focusedTileId` stays purely local — focus is per-client, not
 * synced. There is no setLayout/initSingleTile imperative path: the
 * canonical state is on the hub, seeded by `CreateWorkspace` via the
 * lifecycle outbox; restoration on workspace switch happens via
 * `WatchUser` re-bootstrap.
 *
 * The store is workspace-agnostic. `root` is a slice of the shared user-wide
 * projection selected by `getWorkspaceId`, and focus is held per workspace so
 * switching away and back restores it without a snapshot. Mutators are
 * unaffected: they address nodes by globally-unique CRDT id and never need to
 * know which workspace a tile belongs to.
 */
export interface CreateLayoutStoreOpts {
  /** Which workspace `state.root` and `state.focusedTileId` answer for. */
  getWorkspaceId: () => string | null
  /**
   * Shared user-wide projection. Taking the slice from here rather than
   * projecting one workspace per store keeps `buildChildIndex` to one pass per
   * tick instead of one per workspace.
   */
  projection: () => Projection | null
}

export function createLayoutStore(opts: CreateLayoutStoreOpts) {
  // Per-store fallback generator for test harnesses where the
  // bridge isn't wired — only used to mint a placeholder leaf id so
  // the store's initial render doesn't crash. In production every
  // node id is minted by the CRDT op-emitter via `~/lib/crdt/ops`.
  const generateTileId = makeIdGenerator('tile')
  const initialFallbackTileId = generateTileId()
  const FALLBACK_LEAF: LeafNode = { type: 'leaf', id: initialFallbackTileId }

  // Per workspace, and unset until the user focuses something: the public
  // `focusedTileId()` getter falls back to `firstLeafId(projectedRoot())` on
  // read. That keeps the focused tile aligned with whatever the projection
  // currently shows (placeholder when there's no bridge, real root tile when one
  // is installed) instead of pinning to the locally-minted placeholder.
  //
  // Keyed by workspace so a switch away and back restores focus. This used to
  // ride along in the registry snapshot; with no snapshots it lives here.
  const [focusByWorkspace, setFocusByWorkspace] = createSignal<Record<string, string | null>>({})
  const focusedTileIdRaw = () => focusByWorkspace()[opts.getWorkspaceId() ?? ''] ?? null
  const setFocusedTileId = (tileId: string | null, workspaceId?: string) => {
    const wsId = workspaceId ?? opts.getWorkspaceId() ?? ''
    setFocusByWorkspace(prev => ({ ...prev, [wsId]: tileId }))
  }
  /** The one empty answer `tileOrderFor` hands out for an unknown workspace. */
  const EMPTY_TILE_ORDER: readonly string[] = []

  /**
   * Every workspace's projected tree in local shape, converted ONCE per tick.
   *
   * Four accessors need this — `projectedRoot` for the active workspace, plus
   * `firstLeafIdFor`, `focusedLeafIdFor` and `tileOrderFor` for any workspace —
   * and each used to run `renderTreeToLocal` itself, so a W-workspace sidebar
   * paid W conversions per accessor per tick. Only one of them had a cache, a
   * bespoke element-wise one with its own hand-written eviction.
   *
   * Identity is preserved per workspace across ticks when the tree is
   * structurally unchanged. That is what `tileOrderFor`'s "returns the SAME
   * array" promise rests on, and it comes from one place instead of a second
   * caching mechanism: `renderTreeToLocal` hands back the same node for a
   * `RenderTree` the projection cache reused, and `sameLayoutNode` covers the
   * rest: the uncached `project(state)` the unit tests call, and -- the case
   * only this layer can see -- a change to a `RenderTree` field that
   * `LayoutNodeLocal` DISCARDS, where `sameRenderTree` reports a difference and
   * the projected shape has none (a leaf drops ratios/direction/rows/cols, and
   * UNSPECIFIED and HORIZONTAL both map to 'horizontal'). NOT the same-value
   * register rewrite, despite what this used to say: `rebuildTree`'s own value
   * compare already collapses that one. `getAllTileIds` results hang off a
   * WeakMap keyed by that stable node, so they self-evict with it and need no
   * sweep of their own.
   */
  let previousLocalTrees = new Map<string, LayoutNodeLocal>()
  const tileIdsByNode = new WeakMap<LayoutNodeLocal, readonly string[]>()

  const localTrees = createMemo<Map<string, LayoutNodeLocal>>(() => {
    const proj = opts.projection()
    if (!proj) {
      previousLocalTrees = new Map()
      return previousLocalTrees
    }
    const out = new Map<string, LayoutNodeLocal>()
    let reused = true
    for (const [wsId, ws] of proj.workspaces) {
      const fresh = renderTreeToLocal(ws.mainTree, sharedTrees)
      if (!fresh)
        continue
      const prev = previousLocalTrees.get(wsId)
      const node = prev && sameLayoutNode(prev, fresh) ? prev : fresh
      reused &&= node === prev
      out.set(wsId, node)
    }
    // Hand back the IDENTICAL map when every workspace reused its node and none
    // came or went, so this memo's default `===` stops propagating on a tick
    // that changed nothing here -- extending the projection cache's "an
    // unchanged tick returns the identical object" guarantee through this
    // layer instead of stopping it at `project()`. A drag in one workspace
    // leaves the others' subscribers alone, and a floating-window-only frame
    // leaves every main tree's alone.
    if (reused && out.size === previousLocalTrees.size)
      return previousLocalTrees
    // Otherwise the fresh map becomes the generation: a workspace that left the
    // projection is simply absent from it, so replacing it wholesale IS the
    // eviction (same generation swap as `ProjectionCache.commit`).
    previousLocalTrees = out
    return out
  })

  const localTreeFor = (workspaceId: string): LayoutNodeLocal | undefined =>
    localTrees().get(workspaceId)

  // FROZEN, not merely typed `readonly`. This array is handed to every caller
  // that asks about the same node -- `getAllTileIds`, `tileOrderFor`, and any
  // workspace sharing that subtree -- so an in-place `sort()`/`reverse()` by one
  // of them silently reorders everyone else's answer, for the lifetime of the
  // node. `readonly` makes that a compile error for TS callers and nothing at
  // all for a JS-boundary consumer; the freeze makes it impossible for both.
  // Paid once per node, not per call, because the result is memoized.
  const tileIdsFor = (node: LayoutNodeLocal): readonly string[] => {
    const cached = tileIdsByNode.get(node)
    if (cached)
      return cached
    const ids = Object.freeze(getAllTileIds(node))
    tileIdsByNode.set(node, ids)
    return ids
  }

  // `equals: sameLayoutNode` keeps this memo's non-propagation guarantee
  // self-contained rather than resting on `localTrees` continuing to dedupe:
  // without it, an invalidation here drags the whole downstream tree walk
  // (`allTileIds`, `hasMultipleTiles`, TileRenderer's predicate map, the focus
  // invariant) along with it. Same reason `tabView`'s `placedTabs` carries
  // `equals: samePlacements`. See `localTrees` above for why identity alone is
  // not enough -- the uncached `project(state)` the unit tests call, and a
  // change to a `RenderTree` field that `LayoutNodeLocal` discards. NOT the
  // same-value register rewrite, which `rebuildTree`'s own value compare
  // collapses before it ever reaches this layer.
  const projectedRoot = createMemo<LayoutNodeLocal>(() => {
    const wsId = opts.getWorkspaceId()
    if (!wsId)
      return FALLBACK_LEAF
    return localTreeFor(wsId) ?? FALLBACK_LEAF
  }, FALLBACK_LEAF, { equals: sameLayoutNode })

  // Through `tileIdsFor`, not `getAllTileIds`: the active workspace asks the
  // same question `tileOrderFor` does, so it shares the one WeakMap entry
  // instead of walking the tree a second time and handing out a second array.
  const allTileIdsMemo = createMemo(() => tileIdsFor(projectedRoot()))
  const hasMultipleTilesMemo = createMemo(() => hasMultipleLeaves(projectedRoot()))

  // Focus invariant is NOT enforced here. `focusedTileId` may legally
  // point at a tile owned by `floatingWindowStore`, which this store
  // can't see. `useFocusInvariant` lives a layer up where both stores
  // are visible and snaps focus to the first main leaf only when the
  // tile is gone from BOTH the main tree and every floating window.

  const splitTile = (tileId: string, direction: SplitOrientation): string | null =>
    withBridge((bridge) => {
      if (!containsTileId(projectedRoot(), tileId))
        return null
      return emitSplitTile(bridge, tileId, direction)?.childB ?? null
    }, null)

  const makeGrid = (tileId: string, rows: number, cols: number): { gridId: string, cellTileIds: string[] } => {
    const empty = { gridId: '', cellTileIds: [] }
    return withBridge((bridge) => {
      if (!containsTileId(projectedRoot(), tileId))
        return empty
      return emitMakeGrid(bridge, tileId, rows, cols) ?? empty
    }, empty)
  }

  const removeGrid = (gridId: string): void => {
    withBridge((bridge) => {
      emitRemoveGrid(bridge, gridId)
    }, undefined as void)
  }

  const replaceGridWithLeaf = (gridId: string): string | null =>
    withBridge(bridge => emitReplaceGridWithLeaf(bridge, gridId), null)

  const layoutOwner: LayoutOwner = {
    collectTileIdsInGrid: (gridId) => {
      const grid = findGridById(projectedRoot(), gridId)
      return grid ? getAllTileIds(grid) : []
    },
    findHeirTile: tileId => findHeirTileId(projectedRoot(), tileId),
    firstLeafId: () => firstLeafId(projectedRoot()) ?? null,
    splitTile: (tileId, direction) => { splitTile(tileId, direction) },
    makeGrid: (tileId, rows, cols) => { makeGrid(tileId, rows, cols) },
    removeGrid,
    replaceGridWithLeaf,
  }

  return {
    get state(): LayoutStoreState {
      return {
        get root() { return projectedRoot() },
        get focusedTileId() { return focusedTileIdRaw() },
      } as LayoutStoreState
    },

    /**
     * Focus a tile.
     *
     * `workspaceId` names which workspace's focus slot to write and defaults to
     * the active one. Pass it explicitly whenever the tile belongs to a
     * workspace the user is not currently looking at — a cross-workspace move
     * focuses a tile in the DESTINATION, and filing that under the active
     * workspace's key leaves the on-screen workspace focused on a tile that is
     * not in its tree, which `useFocusInvariant` then resets to the first leaf.
     */
    setFocusedTile(tileId: string | null, workspaceId?: string) {
      const targetWs = workspaceId ?? opts.getWorkspaceId() ?? ''
      if ((focusByWorkspace()[targetWs] ?? null) === tileId)
        return
      setFocusedTileId(tileId, targetWs)
    },

    /**
     * Focused tile for an arbitrary workspace, or null if the user has not
     * focused one there yet. Unlike `focusedTileId()` this does NOT fall back
     * to the first leaf: callers use it to ask "did the user pick a tile in
     * that workspace", and a synthesised answer would be a wrong yes.
     */
    focusedTileIdFor(workspaceId: string): string | null {
      return focusByWorkspace()[workspaceId] ?? null
    },

    /**
     * First leaf in `workspaceId`'s projected tree, in render order.
     *
     * Any workspace, not just the one this store projects — every tree is in
     * the shared projection. Callers landing a tab in another workspace need a
     * LEAF: `tile_id` may only name one, and pointing it at a SPLIT or GRID
     * produces a batch the hub rejects, which reverts the move and drops the
     * drag with no visible error. The workspace's `rootNodeId` is NOT a
     * substitute — it is a SPLIT the moment the user splits their root.
     */
    firstLeafIdFor(workspaceId: string): string | null {
      const local = localTreeFor(workspaceId)
      return local ? firstLeafId(local) ?? null : null
    },

    /**
     * Leaf ids of `workspaceId`'s projected tree, in render order.
     *
     * Lives here rather than in `AppShell` because it is the fourth "read
     * another workspace's projected tree" call site, and the other three
     * (`projectedRoot`, `firstLeafIdFor`, `focusedLeafIdFor`) are already
     * this store's job — an owner that can share the memo, rather than a
     * component keeping its own layout cache.
     *
     * Returns the SAME array while the order is unchanged, so an untouched
     * workspace keeps its identity across ticks — handing back a new array would
     * invalidate `WorkspaceTabTree`'s `buildTree` memo for every sidebar row
     * every tick. It rides on `localTrees`' per-workspace identity rather than
     * caching anything itself. Entries for workspaces that leave the projection
     * are dropped, so this cannot grow one row per workspace ever viewed.
     */
    tileOrderFor(workspaceId: string): readonly string[] {
      const local = localTreeFor(workspaceId)
      return local ? tileIdsFor(local) : EMPTY_TILE_ORDER
    },

    /**
     * Drop remembered focus for every workspace not in `live`.
     *
     * Ids are minted from the CRDT and never reused, so an entry for a deleted
     * workspace can never be hit again and would sit here for the life of the
     * page. Every other per-workspace / per-window map is swept for exactly
     * this reason: `tabSelection` sweeps `activeByWorkspace`/`activeByTile`,
     * `floatingWindow.store` GCs `zOrder`/`focusByWindow`.
     *
     * The counterpart to `tabSelection.retainOnly`, and driven the same way —
     * by `useLayoutFocusSweep`, a projection-gated effect. It used to be a
     * WRITE hidden inside `tileOrderFor`, a read accessor called during render
     * from every sidebar row: that both wrote a signal mid-evaluation of other
     * computations and subscribed every row's tile-order memo to the focus map,
     * so focusing a tile in one workspace invalidated the tile order of all of
     * them. `useLayoutFocusSweep` owns the non-null-projection precondition,
     * because sweeping before the bootstrap lands would erase focus
     * `restoreTabSelection` had just written.
     */
    retainFocusOnly(live: ReadonlySet<string>) {
      const stale = Object.keys(focusByWorkspace()).filter(id => !live.has(id))
      if (stale.length === 0)
        return
      setFocusByWorkspace((prev) => {
        const next = { ...prev }
        for (const id of stale)
          delete next[id]
        return next
      })
    },

    /**
     * The focused tile of `workspaceId`, but ONLY when it is still a live leaf
     * in that workspace's projected tree — otherwise null.
     *
     * The raw pointer in `focusByWorkspace` has no liveness guarantee:
     * `useFocusInvariant` repairs only the ACTIVE workspace, so another
     * workspace's remembered tile can name one that was since closed or turned
     * into a SPLIT by another client. Anything feeding a stored id to `tile_id`
     * must come through here — the hub rejects a non-leaf placement, which
     * silently reverts the move with no visible error. Making the validated
     * read the only way to get the value is what stops the next caller from
     * forgetting, which the raw-pointer-plus-separate-check shape did not.
     *
     * Deliberately NOT folded into `focusedTileIdFor`. That pointer legitimately
     * names a FLOATING-WINDOW tile — `tileLifecycle.focusTile` writes one — and
     * such a tile is absent from `mainTree` but must still be persisted by
     * `useTabPersistence`. The two consumers want different notions of valid,
     * so one healed accessor cannot serve both.
     */
    focusedLeafIdFor(workspaceId: string): string | null {
      const tileId = focusByWorkspace()[workspaceId] ?? null
      if (!tileId)
        return null
      const local = localTreeFor(workspaceId)
      // `containsTileId`, not `getAllTileIds(...).includes(...)`: it early-returns
      // on the first match instead of materialising every leaf id first. Both
      // match leaves only, so the answer is identical.
      return local && containsTileId(local, tileId) ? tileId : null
    },

    focusedTileId(): string {
      return focusedTileIdRaw() ?? firstLeafId(projectedRoot()) ?? ''
    },

    splitTile,

    makeGrid,

    removeGrid,

    replaceGridWithLeaf,

    updateGridRatios(gridId: string, axis: GridAxis, ratios: number[]): boolean {
      return withBridge(bridge => emitUpdateGridRatios(bridge, gridId, axis, ratios), false)
    },

    closeTile(tileId: string) {
      withBridge((bridge) => {
        // Don't close the workspace root — the hub validator rejects
        // root_node_protected and the user-visible behavior is "the
        // workspace's last tile stays open".
        const root = projectedRoot()
        if (root.id === tileId && !hasMultipleLeaves(root))
          return
        emitCloseTile(bridge, tileId)
      }, undefined as void)
    },

    updateRatios(splitId: string, ratios: number[]): boolean {
      return withBridge(bridge => emitUpdateRatios(bridge, splitId, ratios), false)
    },

    /**
     * Leaf tile ids of the CURRENT workspace's tree, in tree order.
     *
     * `readonly` because the array is the shared `tileIdsFor` entry for that
     * node -- the same one `tileOrderFor` hands out -- so a caller that sorted
     * it in place would reorder everyone else's answer.
     */
    getAllTileIds(): readonly string[] {
      return allTileIdsMemo()
    },

    /**
     * Is `tileId` a real tile of the CURRENT workspace's projected tree?
     *
     * The distinction that matters is against `FALLBACK_LEAF`: `projectedRoot`
     * substitutes a locally-minted leaf whenever the projection has no tree, so
     * `focusedTileId()` answers with an id the hub has never heard of rather
     * than with nothing. Anything about to EMIT against a tile has to ask this
     * first -- an op naming that node is rejected, and a caller that already
     * created a worker resource is left holding an orphan.
     */
    hasProjectedTile(tileId: string): boolean {
      return localTreeFor(opts.getWorkspaceId() ?? '') !== undefined
        && containsTileId(projectedRoot(), tileId)
    },

    /** True iff the layout has at least two leaves. */
    hasMultipleTiles(): boolean {
      return hasMultipleTilesMemo()
    },

    owner: () => layoutOwner,
  }
}

/**
 * Reclaim `focusByWorkspace` entries for workspaces that have left the
 * projection.
 *
 * The third of three projection-gated sweeps, alongside `useSelectionSweep` and
 * `useMetadataSweep`, and shaped identically so the next per-workspace map has
 * one pattern to copy. This one used to ride inside `tileOrderFor`, a read
 * accessor — see `retainFocusOnly` for what that cost.
 *
 * Gated on a non-null projection: sweeping before the bootstrap lands would
 * erase the focus `restoreTabSelection` just restored, since every workspace
 * looks dead when the projection is empty.
 */
export function useLayoutFocusSweep(
  projection: () => Projection | null,
  layoutStore: ReturnType<typeof createLayoutStore>,
): void {
  const live = createMemo<ReadonlySet<string> | null>(() => {
    const proj = projection()
    return proj ? new Set(proj.workspaces.keys()) : null
  }, null, {
    // Membership only: the projection is rebuilt every tick, but which
    // workspaces exist changes far more rarely.
    equals: (a, b) => a === b || (!!a && !!b && sameKeys(a, b)),
  })

  createEffect(() => {
    const ids = live()
    if (ids)
      layoutStore.retainFocusOnly(ids)
  })
}
