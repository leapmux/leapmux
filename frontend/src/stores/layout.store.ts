import type { LayoutOwner } from './layoutOwner'
import { createMemo, createSignal } from 'solid-js'
import { getCRDTBridge, projectWorkspace, renderTreeToLocal, withBridge } from '~/lib/crdt'
import { makeIdGenerator } from '~/lib/idGenerator'
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
// canonical state is `OrgCrdtState`; `LayoutNodeLocal` is what the
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
 * `findGridById` / `findSplitById`.
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

export function findSplitById(root: LayoutNodeLocal, splitId: string): SplitNode | null {
  return findNodeByTypeAndId(root, 'split', splitId)
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

// --- Focus invariants ---

const EMPTY_TILE_IDS: ReadonlySet<string> = new Set()

/**
 * Compute the next `focusedTileId` after a disposal-style structural
 * change. `disposedTileIds` is the set of leaf ids that no longer
 * exist in `newRoot`. `replacement` is the tile id to land on iff the
 * current focus was inside that set.
 */
export function nextFocusAfterDisposal(
  newRoot: LayoutNodeLocal,
  currentFocus: string | null,
  disposedTileIds: ReadonlySet<string>,
  replacement: string | null,
): string | null {
  if (currentFocus !== null && disposedTileIds.has(currentFocus) && replacement !== null)
    return replacement
  if (currentFocus !== null && containsTileId(newRoot, currentFocus))
    return currentFocus
  return firstLeafId(newRoot) ?? null
}

/**
 * Convenience for non-grid paths (`closeTile`, `setLayout`) that
 * don't carry a disposal set: keep `currentFocus` when still valid,
 * otherwise fall back to the first leaf.
 */
export function nextFocusEnsuringValid(
  newRoot: LayoutNodeLocal,
  currentFocus: string | null,
): string | null {
  return nextFocusAfterDisposal(newRoot, currentFocus, EMPTY_TILE_IDS, null)
}

// --- Close-tile result shape (consumed by floatingWindow.store) ---

export type CloseTileResult
  = | { kind: 'noop' }
    | { kind: 'changed' }
    | { kind: 'disposed', tileIds: ReadonlySet<string> }

// --- Snapshot helper for backward-compat ---

export function cloneNode(node: LayoutNodeLocal): LayoutNodeLocal {
  if (node.type === 'leaf')
    return { type: 'leaf', id: node.id }
  if (node.type === 'grid') {
    return {
      type: 'grid',
      id: node.id,
      rows: node.rows,
      cols: node.cols,
      rowRatios: [...node.rowRatios],
      colRatios: [...node.colRatios],
      cells: node.cells.map(c => cloneNode(c)),
    }
  }
  return {
    type: 'split',
    id: node.id,
    direction: node.direction,
    ratios: [...node.ratios],
    children: node.children.map(c => cloneNode(c)),
  }
}

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
 * `WatchOrg` re-bootstrap.
 */
export function createLayoutStore() {
  // Per-store fallback generator for test harnesses where the
  // bridge isn't wired — only used to mint a placeholder leaf id so
  // the store's initial render doesn't crash. In production every
  // node id is minted by the CRDT op-emitter via `~/lib/crdt/ops`.
  const generateTileId = makeIdGenerator('tile')
  const initialFallbackTileId = generateTileId()
  const FALLBACK_LEAF: LeafNode = { type: 'leaf', id: initialFallbackTileId }

  // Start unset; the public `focusedTileId()` getter falls back to
  // `firstLeafId(projectedRoot())` on read. That keeps the focused
  // tile aligned with whatever the projection currently shows
  // (placeholder when there's no bridge, real root tile when one is
  // installed) instead of pinning to the locally-minted placeholder.
  const [focusedTileId, setFocusedTileId] = createSignal<string | null>(null)

  // Derive the local tree shape from the CRDT projection.
  const projectedRoot = createMemo<LayoutNodeLocal>(() => {
    const bridge = getCRDTBridge()
    if (!bridge)
      return FALLBACK_LEAF
    const state = bridge.speculativeState()
    const wsId = bridge.workspaceId()
    if (!state || !wsId)
      return FALLBACK_LEAF
    const ws = projectWorkspace(state, wsId)
    if (!ws)
      return FALLBACK_LEAF
    return renderTreeToLocal(ws.mainTree) ?? FALLBACK_LEAF
  })

  const allTileIdsMemo = createMemo(() => getAllTileIds(projectedRoot()))
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
        get focusedTileId() { return focusedTileId() },
      } as LayoutStoreState
    },

    setFocusedTile(tileId: string | null) {
      if (focusedTileId() === tileId)
        return
      setFocusedTileId(tileId)
    },

    focusedTileId(): string {
      return focusedTileId() ?? firstLeafId(projectedRoot()) ?? ''
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

    getAllTileIds(): string[] {
      return allTileIdsMemo()
    },

    /** True iff the layout has at least two leaves. */
    hasMultipleTiles(): boolean {
      return hasMultipleTilesMemo()
    },

    owner: () => layoutOwner,

    /**
     * snapshot returns the current projected tree. Under the CRDT
     * model, snapshot/restore are mostly no-ops — the canonical
     * state is on the hub and re-bootstraps on workspace
     * reactivation. We still preserve the focused-tile id so the UI
     * doesn't blink during workspaceStoreRegistry hand-off.
     */
    snapshot(): LayoutStoreState {
      return {
        root: cloneNode(projectedRoot()),
        focusedTileId: focusedTileId(),
      }
    },

    restore(snap: LayoutStoreState) {
      setFocusedTileId(snap.focusedTileId)
    },
  }
}
