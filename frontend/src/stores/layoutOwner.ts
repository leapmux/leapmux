import type { SplitOrientation } from './layout.store'

/**
 * Common interface for "the layout container that owns a tile" — either the
 * main layout or a single floating window. Both `layoutStore.owner()` and
 * `floatingWindowStore.owner(windowId)` return a `LayoutOwner`, so callers
 * can dispatch on tile-id once and then act on the owner without repeating
 * `windowId ? floating.X(windowId, ...) : main.X(...)` branches.
 *
 * Per-tile predicates (closeMode, canSplit, canMakeGrid, gridIdForClose) are
 * not owner methods — callers read them from `buildTilePredicateMap`'s
 * memoized map so a single tree walk serves every tile in a render.
 */
export interface LayoutOwner {
  // Queries
  collectTileIdsInGrid: (gridId: string) => string[]
  /**
   * Tile that should inherit `tileId`'s tabs when it closes — sibling first,
   * walking up the tree as needed. Returns null when no in-owner heir exists
   * (single-leaf root, or `tileId` not in this owner's tree).
   */
  findHeirTile: (tileId: string) => string | null
  /** First leaf id in this owner's tree, or null if the tree is empty. */
  firstLeafId: () => string | null

  // Mutations. Return values are intentionally `void`: callers use the owner
  // to dispatch the action and re-derive any needed state from the store.
  splitTile: (tileId: string, direction: SplitOrientation) => void
  makeGrid: (tileId: string, rows: number, cols: number) => void
  removeGrid: (gridId: string) => void
  replaceGridWithLeaf: (gridId: string) => string | null
}
