import type { LayoutOwner } from './layoutOwner'
import type { LayoutNode } from '~/generated/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { createMemo } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { LayoutGridSchema, LayoutLeafSchema, LayoutNodeSchema, LayoutSplitSchema, SplitDirection } from '~/generated/leapmux/v1/workspace_pb'
import { makeIdGenerator } from '~/lib/idGenerator'

// --- Local types (plain JSON, not proto) ---

/**
 * Orientation of a split node and its resize handles. Surfaces in the
 * renderer as the `data-direction` attribute on the split container and
 * separators (see `TilingLayout.tsx`). The proto layer uses
 * `SplitDirection` (HORIZONTAL/VERTICAL); `fromProto`/`toProto` translate
 * at the boundary.
 */
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
  cells: LayoutNodeLocal[] // length = rows * cols, row-major: cells[r*cols + c]
}

export type GridAxis = 'row' | 'col'

export type LayoutNodeLocal = SplitNode | LeafNode | GridNode

/**
 * Structural children of a layout node. Splits have `children`, grids have
 * `cells`; both are walked the same way for membership / heir / leaf-walk
 * traversals. Leaves return `[]` so callers can iterate uniformly without a
 * dedicated branch.
 */
function childrenOf(node: LayoutNodeLocal): LayoutNodeLocal[] {
  if (node.type === 'leaf')
    return []
  return node.type === 'grid' ? node.cells : node.children
}

/**
 * Map over a node's structural children (grid cells or split children),
 * preserving identity when nothing changes. Returns the original node
 * unchanged if every mapped child is referentially identical to the input
 * — callers rely on this for no-op short-circuiting in recursive walks.
 * Leaves are returned as-is.
 */
function mapChildren(
  node: LayoutNodeLocal,
  fn: (child: LayoutNodeLocal) => LayoutNodeLocal,
): LayoutNodeLocal {
  if (node.type === 'leaf')
    return node
  const src = node.type === 'grid' ? node.cells : node.children
  // Defer array allocation until the first divergent child. Most recursive
  // walks land on tiles unrelated to the mutation; allocating a throwaway
  // array per recursion level would churn GC during ratio drags and grid
  // structural ops.
  let next: LayoutNodeLocal[] | null = null
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    const r = fn(c)
    if (r === c) {
      if (next !== null)
        next.push(r)
      continue
    }
    if (next === null)
      next = src.slice(0, i)
    next.push(r)
  }
  if (next === null)
    return node
  return node.type === 'grid'
    ? { ...node, cells: next }
    : { ...node, children: next }
}

/**
 * Walk a node's structural children with early-exit: returns the parent
 * rebuilt with the first child for which `try_` returns a non-null result
 * (other children are skipped). Use when the recursion only ever modifies
 * one child per parent — `mapChildren` would re-visit subsequent children
 * unnecessarily.
 */
function rebuildWithFirstHit<T>(
  node: LayoutNodeLocal,
  try_: (child: LayoutNodeLocal) => [LayoutNodeLocal, T] | null,
): [LayoutNodeLocal, T] | null {
  if (node.type === 'leaf')
    return null
  const list = node.type === 'grid' ? node.cells : node.children
  for (let i = 0; i < list.length; i++) {
    const result = try_(list[i])
    if (result === null)
      continue
    const [newChild, payload] = result
    const newList = list.map((c, j) => j === i ? newChild : c)
    if (node.type === 'grid')
      return [{ ...node, cells: newList }, payload]
    return [{ ...node, children: newList }, payload]
  }
  return null
}

export interface LayoutStoreState {
  root: LayoutNodeLocal
  focusedTileId: string | null
}

export const MAX_GRID_DIMENSION = 20
const RATIO_TOLERANCE = 0.01
export const MAX_DEPTH = 3

/**
 * Floor for any single entry in a split's `ratios` array or a grid's
 * `rowRatios`/`colRatios`. Resize handles clamp at this so a pane can't be
 * dragged past invisibility.
 */
export const MIN_SPLIT_RATIO = 0.05

// Used only when `fromProto` hits a malformed proto branch (none of
// leaf/split/grid). Store factories pass their own per-instance generator
// in; this default keeps `fromProto` usable from tests without one.
const fallbackTileIdGenerator = makeIdGenerator('tile')

// --- Proto conversion ---

export function fromProto(
  node: LayoutNode,
  generateId: () => string = fallbackTileIdGenerator,
): LayoutNodeLocal {
  if (node.node.case === 'leaf') {
    return { type: 'leaf', id: node.node.value.id }
  }
  if (node.node.case === 'split') {
    const s = node.node.value
    return {
      type: 'split',
      id: s.id,
      direction: s.direction === SplitDirection.VERTICAL ? 'vertical' : 'horizontal',
      ratios: [...s.ratios],
      children: s.children.map(c => fromProto(c, generateId)),
    }
  }
  if (node.node.case === 'grid') {
    const g = node.node.value
    return {
      type: 'grid',
      id: g.id,
      rows: g.rows,
      cols: g.cols,
      rowRatios: [...g.rowRatios],
      colRatios: [...g.colRatios],
      cells: g.cells.map(c => fromProto(c, generateId)),
    }
  }
  // Fallback: create a default leaf. Reaching this branch means the proto
  // arrived without one of the three known node cases, which indicates a
  // backend bug (or a forward-incompatible proto). Surface it instead of
  // silently swallowing.
  console.warn('[layout.store] fromProto: malformed LayoutNode (unknown case), falling back to a default leaf', node)
  return { type: 'leaf', id: generateId() }
}

export function toProto(node: LayoutNodeLocal): LayoutNode {
  if (node.type === 'leaf') {
    const leaf = create(LayoutLeafSchema, { id: node.id })
    return create(LayoutNodeSchema, { node: { case: 'leaf' as const, value: leaf } })
  }
  if (node.type === 'grid') {
    const g = create(LayoutGridSchema, {
      id: node.id,
      rows: node.rows,
      cols: node.cols,
      rowRatios: [...node.rowRatios],
      colRatios: [...node.colRatios],
      cells: node.cells.map(c => toProto(c)),
    })
    return create(LayoutNodeSchema, { node: { case: 'grid' as const, value: g } })
  }
  const split = create(LayoutSplitSchema, {
    id: node.id,
    direction: node.direction === 'vertical' ? SplitDirection.VERTICAL : SplitDirection.HORIZONTAL,
    ratios: [...node.ratios],
    children: node.children.map(c => toProto(c)),
  })
  return create(LayoutNodeSchema, { node: { case: 'split' as const, value: split } })
}

// --- Optimization ---

export function optimize(node: LayoutNodeLocal): LayoutNodeLocal {
  if (node.type === 'leaf')
    return node

  // Recursively optimize structural children first. `mapChildren` returns
  // the original node when nothing changed, or a freshly spread node
  // otherwise — and preserves the node type, so a grid stays a grid and a
  // split stays a split. Grids are structurally canonical (no flatten /
  // unwrap), so we early-exit here. Splits fall through to the
  // flatten-and-unwrap pass below.
  const mapped = mapChildren(node, optimize)
  if (mapped.type !== 'split')
    return mapped
  const optimizedChildren = mapped.children

  // Unwrap single-child split
  if (optimizedChildren.length === 1)
    return optimizedChildren[0]

  // Flatten same-direction nesting
  const newChildren: LayoutNodeLocal[] = []
  const newRatios: number[] = []
  let flattened = false

  for (let i = 0; i < optimizedChildren.length; i++) {
    const child = optimizedChildren[i]
    if (child.type === 'split' && child.direction === mapped.direction) {
      const parentRatio = mapped.ratios[i]
      for (let j = 0; j < child.children.length; j++) {
        newChildren.push(child.children[j])
        newRatios.push(parentRatio * child.ratios[j])
      }
      flattened = true
    }
    else {
      newChildren.push(child)
      newRatios.push(mapped.ratios[i])
    }
  }

  if (flattened && newChildren.length === 1)
    return newChildren[0]

  if (flattened)
    return { ...mapped, children: newChildren, ratios: newRatios }

  return mapped
}

// --- Helper: collect all leaf tile IDs ---

export function getAllTileIds(node: LayoutNodeLocal): string[] {
  if (node.type === 'leaf')
    return [node.id]
  return childrenOf(node).flatMap(getAllTileIds)
}

/**
 * Membership test that early-returns instead of materialising the full leaf
 * id array. Use for hot-path "does this tree contain `tileId`" checks.
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
 * True iff the tree has at least two leaves. Walks until the second leaf
 * is found and stops, instead of materialising the full id array via
 * `getAllTileIds(node).length > 1`.
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
 * Pick the leaf tile that should inherit `closingTileId`'s tabs when the tile
 * is closed. Walks up from the closing leaf to its first ancestor with a
 * sibling subtree, then returns the first leaf in that adjacent sibling
 * (preferring the left/upper neighbor, falling back to the right/lower).
 *
 * Returns null when `closingTileId` is the only leaf in `root` — callers
 * decide where to fall back (e.g. a floating-window tile defers to the main
 * layout's first tile).
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
 * Walk the leftmost descent and return the first leaf id. Returns undefined
 * only if the tree is malformed (a split or grid with no children) — every
 * well-formed tree has at least one leaf. Use this for "first leaf" lookups
 * to avoid materialising the full id array via `getAllTileIds(...)[0]`.
 */
export function firstLeafId(node: LayoutNodeLocal): string | undefined {
  if (node.type === 'leaf')
    return node.id
  const first = childrenOf(node)[0]
  return first ? firstLeafId(first) : undefined
}

// --- Helper: find and replace a node by tile ID ---

export function replaceNode(
  root: LayoutNodeLocal,
  tileId: string,
  replacer: (leaf: LeafNode) => LayoutNodeLocal,
): LayoutNodeLocal {
  if (root.type === 'leaf')
    return root.id === tileId ? replacer(root) : root
  return mapChildren(root, c => replaceNode(c, tileId, replacer))
}

// --- Helper: find a node by type + id ---

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

// --- Helper: find and replace a grid by id ---

export function replaceGridById(
  root: LayoutNodeLocal,
  gridId: string,
  replacer: (grid: GridNode) => LayoutNodeLocal,
): LayoutNodeLocal {
  if (root.type === 'leaf')
    return root
  if (root.type === 'grid' && root.id === gridId)
    return replacer(root)
  return mapChildren(root, c => replaceGridById(c, gridId, replacer))
}

// --- Helper: add a sibling to an existing same-direction split ---
// Returns [newRoot, true] if the tile's parent split matches the direction
// and the sibling was added; [root, false] otherwise. The helper descends
// into grid cells looking for a same-direction split deeper in the tree, but
// never inserts a sibling at the grid level (grids have a fixed shape).

export function addSiblingInSameDirectionSplit(
  root: LayoutNodeLocal,
  tileId: string,
  newTileId: string,
  direction: SplitOrientation,
): [LayoutNodeLocal, boolean] {
  if (root.type === 'leaf')
    return [root, false]

  // Same-direction split with the tile as a direct child: insert sibling
  // in place. Grids never match this fast path — they have a fixed shape
  // and only descend.
  if (root.type === 'split' && root.direction === direction) {
    const childIndex = root.children.findIndex(
      c => c.type === 'leaf' && c.id === tileId,
    )
    if (childIndex >= 0) {
      const newChildren = [...root.children]
      newChildren.splice(childIndex + 1, 0, { type: 'leaf', id: newTileId })
      const equalRatio = 1 / newChildren.length
      return [{
        ...root,
        children: newChildren,
        ratios: newChildren.map(() => equalRatio),
      }, true]
    }
  }

  // Otherwise descend; the first hit short-circuits the walk so we don't
  // mutate more than one child per parent.
  const hit = rebuildWithFirstHit(root, (child) => {
    const [newChild, found] = addSiblingInSameDirectionSplit(child, tileId, newTileId, direction)
    return found ? [newChild, true as const] : null
  })
  return hit ?? [root, false]
}

/**
 * Apply a per-child transform to a split's children, dropping `null` results,
 * collapsing to the sole survivor, and renormalising ratios. Returns the
 * original split unchanged when every child stays referentially identical;
 * returns `null` when every child was dropped.
 */
function rebuildSplit(
  split: SplitNode,
  transform: (child: LayoutNodeLocal) => LayoutNodeLocal | null,
): LayoutNodeLocal | null {
  const newChildren: LayoutNodeLocal[] = []
  const newRatios: number[] = []
  let changed = false
  for (let i = 0; i < split.children.length; i++) {
    const child = split.children[i]
    const result = transform(child)
    if (result === null) {
      changed = true
      continue
    }
    if (result !== child)
      changed = true
    newChildren.push(result)
    newRatios.push(split.ratios[i])
  }
  if (!changed)
    return split
  if (newChildren.length === 0)
    return null
  if (newChildren.length === 1)
    return newChildren[0]
  const sum = newRatios.reduce((a, b) => a + b, 0)
  return { ...split, children: newChildren, ratios: newRatios.map(r => r / sum) }
}

// --- Helper: remove a leaf by id ---
//
// Walks the tree and removes the leaf with the given id. For splits, the
// usual collapse-on-1-child + ratio-renormalisation rules apply. For grids,
// emptying a cell does NOT delete the cell — the cell is replaced with a
// fresh empty leaf produced by `idFactory`. This preserves the grid's
// rows × cols shape.

export function removeNode(
  root: LayoutNodeLocal,
  tileId: string,
  idFactory: () => string,
): LayoutNodeLocal | null {
  if (root.type === 'leaf')
    return root.id === tileId ? null : root

  if (root.type === 'grid') {
    // Grids keep their shape: an emptied cell becomes a fresh leaf rather
    // than collapsing the grid. `mapChildren` short-circuits to the same
    // grid reference when every cell is identity-equal.
    return mapChildren(root, (cell) => {
      const result = removeNode(cell, tileId, idFactory)
      return result ?? { type: 'leaf' as const, id: idFactory() }
    })
  }

  return rebuildSplit(root, child => removeNode(child, tileId, idFactory))
}

// --- Helper: validate ratio arrays before applying them ---

function ratiosAreValid(ratios: number[], expectedLength: number): boolean {
  if (ratios.length !== expectedLength)
    return false
  let sum = 0
  for (const r of ratios) {
    if (!Number.isFinite(r) || r <= 0)
      return false
    sum += r
  }
  return Math.abs(sum - 1) <= RATIO_TOLERANCE
}

/**
 * Element-wise approximate equality. Used inside `applySplitRatios` /
 * `applyGridRatios` as a no-op guard: callers that pass ratios identical
 * to the current state (modulo float fuzz) skip the in-place mutation,
 * which avoids spurious downstream re-renders. Defense-in-depth — the
 * renderers already commit only on pointer-up — but cheap and useful.
 */
function ratiosApproxEqual(a: readonly number[], b: readonly number[]): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    if (Math.abs(a[i] - b[i]) > 1e-9)
      return false
  }
  return true
}

/**
 * Validate `ratios` against the grid `gridId` inside `root` and write them
 * in place when valid and not a no-op. Designed to be called inside a Solid
 * `produce()` callback. Returns `true` iff the tree was mutated; the wrapper
 * uses that to gate eager no-op short-circuits (avoids the path-level emit
 * and proxy allocation that an unconditional setState would incur).
 */
export function applyGridRatios(
  root: LayoutNodeLocal,
  gridId: string,
  axis: GridAxis,
  ratios: number[],
): boolean {
  const grid = findGridById(root, gridId)
  if (!grid)
    return false
  const expectedLen = axis === 'row' ? grid.rows : grid.cols
  if (!ratiosAreValid(ratios, expectedLen))
    return false
  const current = axis === 'row' ? grid.rowRatios : grid.colRatios
  if (ratiosApproxEqual(current, ratios))
    return false
  if (axis === 'row')
    grid.rowRatios = [...ratios]
  else
    grid.colRatios = [...ratios]
  return true
}

/** Same as `applyGridRatios`, for splits. */
export function applySplitRatios(
  root: LayoutNodeLocal,
  splitId: string,
  ratios: number[],
): boolean {
  const split = findSplitById(root, splitId)
  if (!split)
    return false
  if (!ratiosAreValid(ratios, split.children.length))
    return false
  if (ratiosApproxEqual(split.ratios, ratios))
    return false
  split.ratios = [...ratios]
  return true
}

// --- Pure tree rewrites for grid mutations ---

/**
 * Build a `rows × cols` grid in place of the leaf `tileId`. The (0, 0) cell
 * preserves `tileId` so existing tabs keep their tile association; other
 * cells get fresh ids from `idFactory`. Throws on out-of-range dimensions.
 */
export function makeGridInTree(
  root: LayoutNodeLocal,
  tileId: string,
  rows: number,
  cols: number,
  idFactory: () => string,
): { newRoot: LayoutNodeLocal, gridId: string, cellTileIds: string[] } {
  if (!Number.isInteger(rows) || !Number.isInteger(cols)
    || rows < 1 || cols < 1
    || rows > MAX_GRID_DIMENSION || cols > MAX_GRID_DIMENSION) {
    throw new Error(`makeGrid: rows and cols must be integers in [1, ${MAX_GRID_DIMENSION}], got ${rows}x${cols}`)
  }
  const gridId = idFactory()
  const cellTileIds: string[] = []
  const cells: LayoutNodeLocal[] = []
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const cellId = (r === 0 && c === 0) ? tileId : idFactory()
      cellTileIds.push(cellId)
      cells.push({ type: 'leaf', id: cellId })
    }
  }
  const rowRatios: number[] = (Array.from({ length: rows }) as number[]).fill(1 / rows)
  const colRatios: number[] = (Array.from({ length: cols }) as number[]).fill(1 / cols)
  const newRoot = replaceNode(root, tileId, () => ({
    type: 'grid',
    id: gridId,
    rows,
    cols,
    rowRatios,
    colRatios,
    cells,
  }))
  return { newRoot, gridId, cellTileIds }
}

export interface RemoveGridResult {
  newRoot: LayoutNodeLocal
  /** Tile to refocus to when the previous focus was inside the removed grid. */
  refocusTo: string | null
  /** All tile ids that were inside the removed grid (for focus-membership tests). */
  oldTileIds: Set<string>
}

/**
 * Remove an entire grid from the tree. Behaviour by parent type:
 *  - root grid    → tree becomes a fresh leaf
 *  - parent grid  → grid's slot becomes a fresh leaf
 *  - parent split → split drops the grid + optimize collapses 2-way splits
 * Returns null if the grid is not in the tree.
 */
export function removeGridInTree(
  root: LayoutNodeLocal,
  gridId: string,
  idFactory: () => string,
): RemoveGridResult | null {
  // Root-grid case: tree becomes a fresh leaf.
  if (root.type === 'grid' && root.id === gridId) {
    const newTileId = idFactory()
    return {
      newRoot: { type: 'leaf', id: newTileId },
      refocusTo: newTileId,
      oldTileIds: new Set(getAllTileIds(root)),
    }
  }

  // Single walk that finds the target grid in the tree and reacts based on
  // its parent: a parent grid swaps the cell slot for a fresh leaf, a parent
  // split drops the child. The two cases are mutually exclusive (a grid has
  // exactly one parent) so they share the same traversal. `replacementId`
  // is only minted on the cell-swap path so the split-drop path doesn't
  // burn an id (and bump the generator counter) for nothing.
  let oldTileIds: Set<string> | null = null
  let replacementId: string | null = null

  const walk = (node: LayoutNodeLocal): LayoutNodeLocal => {
    if (node.type === 'leaf')
      return node
    if (node.type === 'grid') {
      // Match a child grid by id BEFORE recursing — the cell-swap path. A
      // bare `walk(cell)` would descend past the target instead of swapping
      // the cell slot itself.
      return mapChildren(node, (cell) => {
        if (cell.type === 'grid' && cell.id === gridId) {
          oldTileIds = new Set(getAllTileIds(cell))
          replacementId = idFactory()
          return { type: 'leaf' as const, id: replacementId }
        }
        return walk(cell)
      })
    }
    // Split: child grid id-match → drop, else recurse.
    return rebuildSplit(node, (child) => {
      if (child.type === 'grid' && child.id === gridId) {
        oldTileIds = new Set(getAllTileIds(child))
        return null
      }
      return walk(child)
    }) ?? node
  }

  const newRoot = walk(root)
  if (oldTileIds === null)
    return null
  if (replacementId !== null)
    return { newRoot, refocusTo: replacementId, oldTileIds }
  // Split-drop path: optimize collapses 2-way splits whose other child is now
  // the sole survivor.
  const optimized = optimize(newRoot)
  return { newRoot: optimized, refocusTo: firstLeafId(optimized) ?? null, oldTileIds }
}

export interface ReplaceGridWithLeafResult {
  newRoot: LayoutNodeLocal
  newTileId: string
  oldTileIds: Set<string>
}

/**
 * Replace the grid with a single leaf in the same parent slot. Used by
 * "convert grid to tile" so tabs can be reassigned onto the merged tile.
 */
export function replaceGridWithLeafInTree(
  root: LayoutNodeLocal,
  gridId: string,
  idFactory: () => string,
): ReplaceGridWithLeafResult | null {
  const grid = findGridById(root, gridId)
  if (!grid)
    return null
  const oldTileIds = new Set(getAllTileIds(grid))
  const newTileId = idFactory()
  const newRoot = replaceGridById(root, gridId, () => ({ type: 'leaf' as const, id: newTileId }))
  return { newRoot, newTileId, oldTileIds }
}

/**
 * Split-tile algorithm shared by `layoutStore` and `floatingWindowStore`:
 * 1. If `tileId`'s parent is already a split in the same direction, append
 *    `newTileId` as a sibling (renormalizing ratios) and return an
 *    `optimize`d tree.
 * 2. Otherwise, wrap the leaf in a fresh 2-child split with even ratios;
 *    no `optimize` walk is needed (see comment inside).
 * `idFactory` mints the wrapping split's id (only used in branch 2).
 */
export function splitTileInTree(
  root: LayoutNodeLocal,
  tileId: string,
  newTileId: string,
  direction: SplitOrientation,
  idFactory: () => string,
): LayoutNodeLocal {
  const [withSibling, added] = addSiblingInSameDirectionSplit(root, tileId, newTileId, direction)
  if (added)
    return optimize(withSibling)
  // Wrap branch: branch 1 already handled the same-direction-parent case, so
  // the new split's parent (if any) has a different direction or is a grid /
  // root. None of those flatten with the new split, so no `optimize` walk is
  // needed.
  return replaceNode(root, tileId, leaf => ({
    type: 'split' as const,
    id: idFactory(),
    direction,
    ratios: [0.5, 0.5],
    children: [
      { type: 'leaf' as const, id: leaf.id },
      { type: 'leaf' as const, id: newTileId },
    ],
  }))
}

// --- Batched predicate map ---

/**
 * Discriminated union: `'grid'` carries the id of the innermost grid this
 * tile is the close anchor for, captured during the same walk that
 * classified the tile so close handlers don't need a second tree
 * traversal. The other two kinds carry no payload.
 */
export type TileCloseMode
  = | { kind: 'none' }
    | { kind: 'tile' }
    | { kind: 'grid', gridId: string }

export const CLOSE_MODE_NONE: TileCloseMode = { kind: 'none' }
export const CLOSE_MODE_TILE: TileCloseMode = { kind: 'tile' }

/**
 * UI label + testId for the close affordance, derived once from a
 * `TileCloseMode`. Tile and TileActionsMenu both render close buttons but
 * use different testId suffixes (raw `close-grid`/`close-tile` for the
 * tile button, `-menu-item` for the dropdown), so the helper takes a
 * `surface` argument.
 */
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

/**
 * Layout-root kind:
 * - `main`: applies MAX_DEPTH cap on split/grid actions.
 * - `floating`: no depth cap.
 *
 * Both root kinds suppress the per-tile close button when the root contains
 * only one tile — the user closes that tile via the workspace (last-tab
 * flow) or the floating window's chrome close button.
 */
export type LayoutRootKind = 'main' | 'floating'

/**
 * Batched helper that produces all per-tile predicates for a layout root in
 * a single DFS. Tree-wide multiplicity is probed via `hasMultipleLeaves`,
 * which short-circuits at the second leaf — `closeMode='tile'` cares only
 * about >1, not the exact count. Replaces O(N × tree-size) per-render
 * predicate calls with O(N).
 *
 * Anchor semantics: each grid spawns a "visual top-right" descent into its
 * (0, lastCol) cell; horizontal splits follow the rightmost child along that
 * path; vertical splits follow the topmost child. A leaf is a close-anchor
 * iff it sits at the end of any such active descent — naïve "all descendants
 * of cell[lastCol]" is wrong because inner splits inside the top-right cell
 * narrow the path further.
 */
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

/**
 * Walk-invariant context shared by every recursive call in
 * `walkPredicates` (close-anchor logic + the output sink). The varying
 * descent state — `depth`, `innermostAnchorGridId`, `isDirectGridCell` —
 * is threaded as positional args so the recursion stays cheap to read.
 */
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
    // Entering this grid's anchor cell starts a new top-right descent rooted
    // at this grid; non-anchor cells terminate any in-progress descent.
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
  // split: anchor child preserves the active descent; siblings end it.
  const lastIdx = node.children.length - 1
  const anchorChildIdx = node.direction === 'horizontal' ? lastIdx : 0
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

// --- Shared focus update ---

const EMPTY_TILE_IDS: ReadonlySet<string> = new Set()

/**
 * Compute the next `focusedTileId` after a disposal-style structural change
 * (close-tile, remove-grid, replace-grid-with-leaf, …). `disposedTileIds`
 * is the set of leaf ids that no longer exist in `newRoot`. `replacement`
 * is the tile id to land on iff the current focus was inside that set; the
 * grid mutators surface it as `refocusTo` / `newTileId`. Pass `null` when
 * there's no preferred replacement and the helper should keep the existing
 * focus (if still valid) or fall back to the first leaf.
 *
 * Used by both `layout.store` (single root) and `floatingWindow.store`
 * (per-window root) so the focus invariant is computed once.
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
 * Convenience for non-grid paths (`closeTile`, `setLayout`) that don't
 * carry a disposal set: keep `currentFocus` when still valid, otherwise
 * fall back to the first leaf. Implemented as a thin wrapper so the
 * "first leaf if all else fails" invariant lives in one place.
 */
export function nextFocusEnsuringValid(
  newRoot: LayoutNodeLocal,
  currentFocus: string | null,
): string | null {
  return nextFocusAfterDisposal(newRoot, currentFocus, EMPTY_TILE_IDS, null)
}

// --- Pure mutation planners (shared between layout.store + floatingWindow.store) ---

/**
 * Read-only slice both stores feed into the planner helpers below. The main
 * layout passes its top-level state; the floating-window store passes one of
 * its `FloatingWindowState`s with `root`/`focusedTileId` aliased to
 * `layoutRoot`/`focusedTileId`. Keeping a structural alias instead of two
 * different parameter shapes lets the planners stay pure and surface-agnostic.
 */
export interface RootFocus {
  readonly root: LayoutNodeLocal
  readonly focusedTileId: string | null
}

/**
 * Adapter input for {@link planCloseTile} and friends. The floating-window
 * store passes a `FloatingWindowState` whose root is `layoutRoot`, not `root`,
 * so we can't accept its raw shape directly. Callers project to this view.
 */
export function rootFocusOf(s: { layoutRoot: LayoutNodeLocal, focusedTileId: string | null }): RootFocus {
  return { root: s.layoutRoot, focusedTileId: s.focusedTileId }
}

/**
 * Plan a close-tile mutation. Returns one of:
 * - `{ kind: 'noop' }`: the tile id wasn't found.
 * - `{ kind: 'empty' }`: removing it would empty the root (caller decides
 *   whether to silently skip or drop the containing window).
 * - `{ kind: 'changed', ...newRootFocus }`: persist the new root + focus.
 *
 * Pure: no store mutation. Both stores call this and route the result
 * through their own setState shape.
 */
export type CloseTilePlan
  = | { kind: 'noop' }
    | { kind: 'empty' }
    | { kind: 'changed', root: LayoutNodeLocal, focusedTileId: string | null }

/**
 * Outcome of `floatingWindowStore.closeTile`. Discriminates on disposal so
 * the caller can branch without re-deriving "is this window gone?" from a
 * separate `getWindow` lookup, and gets the disposed window's leaf-id set
 * (needed to scrub tab-store entries) directly in the result instead of
 * having to pre-snapshot it before the call.
 */
export type CloseTileResult
  = | { kind: 'noop' }
    | { kind: 'changed' }
    | { kind: 'disposed', tileIds: ReadonlySet<string> }

export function planCloseTile(s: RootFocus, tileId: string, idFactory: () => string): CloseTilePlan {
  const result = removeNode(s.root, tileId, idFactory)
  if (result === null)
    return { kind: 'empty' }
  if (result === s.root)
    return { kind: 'noop' }
  const optimized = optimize(result)
  return { kind: 'changed', root: optimized, focusedTileId: nextFocusEnsuringValid(optimized, s.focusedTileId) }
}

/** Plan a remove-grid mutation. Returns null when the grid id isn't in the tree. */
export function planRemoveGrid(s: RootFocus, gridId: string, idFactory: () => string): RootFocus | null {
  const result = removeGridInTree(s.root, gridId, idFactory)
  if (!result)
    return null
  return {
    root: result.newRoot,
    focusedTileId: nextFocusAfterDisposal(result.newRoot, s.focusedTileId, result.oldTileIds, result.refocusTo),
  }
}

/**
 * Plan a replace-grid-with-leaf mutation. Returns null when the grid id
 * isn't in the tree; otherwise the new root + focus and the minted leaf id
 * (which the caller surfaces back so tab merging can target it).
 */
export function planReplaceGridWithLeaf(
  s: RootFocus,
  gridId: string,
  idFactory: () => string,
): (RootFocus & { newTileId: string }) | null {
  const result = replaceGridWithLeafInTree(s.root, gridId, idFactory)
  if (!result)
    return null
  return {
    root: result.newRoot,
    focusedTileId: nextFocusAfterDisposal(result.newRoot, s.focusedTileId, result.oldTileIds, result.newTileId),
    newTileId: result.newTileId,
  }
}

/** Plan a setLayout (replace whole root). Returns null when `node === s.root`. */
export function planSetLayout(s: RootFocus, node: LayoutNodeLocal): RootFocus | null {
  if (node === s.root)
    return null
  return { root: node, focusedTileId: nextFocusEnsuringValid(node, s.focusedTileId) }
}

// --- Store ---

export function createLayoutStore() {
  // Per-store generator: counter resets per workspace so two stores
  // don't share counter state through a module-level singleton. Ids
  // remain unique within one store instance.
  const generateTileId = makeIdGenerator('tile')
  const defaultTileId = generateTileId()
  const [state, setState] = createStore<LayoutStoreState>({
    root: { type: 'leaf', id: defaultTileId },
    focusedTileId: defaultTileId,
  })

  // Memoized leaf-id list. Recomputed only when `state.root` mutates;
  // workspace-restore and other consumers consult this without re-walking
  // the tree per call.
  const allTileIdsMemo = createMemo(() => getAllTileIds(state.root))
  // Cached "has 2+ leaves" probe — read on every <Tile> render to gate the
  // close button. The walk short-circuits on the second leaf, but caching
  // avoids re-walking from root on every render.
  const hasMultipleTilesMemo = createMemo(() => hasMultipleLeaves(state.root))

  // Hoisted method impls. Both the store object and the `LayoutOwner`
  // singleton below reference them, so the owner can be built once at the
  // end of `createLayoutStore` instead of allocated fresh per `owner()`
  // call.
  const splitTile = (tileId: string, direction: SplitOrientation): string | null => {
    // Pre-flight: skip ID generation when the tile is missing (e.g. a race
    // against close). Returning a freshly-minted-but-unused id would still
    // advance the counter and pollute the id space.
    if (!containsTileId(state.root, tileId))
      return null
    const newTileId = generateTileId()
    const newRoot = splitTileInTree(state.root, tileId, newTileId, direction, generateTileId)
    if (newRoot !== state.root)
      setState('root', newRoot)
    return newTileId
  }

  const makeGrid = (tileId: string, rows: number, cols: number): { gridId: string, cellTileIds: string[] } => {
    // Pre-flight: avoid allocating gridId + rows*cols cell ids when the
    // target tile isn't in the tree.
    if (!containsTileId(state.root, tileId))
      return { gridId: '', cellTileIds: [] }
    const { newRoot, gridId, cellTileIds } = makeGridInTree(state.root, tileId, rows, cols, generateTileId)
    setState('root', newRoot)
    return { gridId, cellTileIds }
  }

  const removeGrid = (gridId: string): void => {
    const next = planRemoveGrid(state, gridId, generateTileId)
    if (!next)
      return
    setState(produce((s) => {
      s.root = next.root
      s.focusedTileId = next.focusedTileId
    }))
  }

  const replaceGridWithLeaf = (gridId: string): string | null => {
    const next = planReplaceGridWithLeaf(state, gridId, generateTileId)
    if (!next)
      return null
    setState(produce((s) => {
      s.root = next.root
      s.focusedTileId = next.focusedTileId
    }))
    return next.newTileId
  }

  // Built once and returned by `owner()` on every call so callers (e.g.
  // TileRenderer's `ownerOf` dispatch) get a stable reference. The methods
  // close over the hoisted impls above plus `state.root` / shared helpers.
  const layoutOwner: LayoutOwner = {
    collectTileIdsInGrid: (gridId) => {
      const grid = findGridById(state.root, gridId)
      return grid ? getAllTileIds(grid) : []
    },
    findHeirTile: tileId => findHeirTileId(state.root, tileId),
    firstLeafId: () => firstLeafId(state.root) ?? null,
    splitTile: (tileId, direction) => { splitTile(tileId, direction) },
    makeGrid: (tileId, rows, cols) => { makeGrid(tileId, rows, cols) },
    removeGrid,
    replaceGridWithLeaf,
  }

  return {
    state,

    setLayout(node: LayoutNodeLocal) {
      const next = planSetLayout(state, node)
      if (!next)
        return
      setState(produce((s) => {
        s.root = next.root
        s.focusedTileId = next.focusedTileId
      }))
    },

    setFocusedTile(tileId: string) {
      if (state.focusedTileId === tileId)
        return
      setState('focusedTileId', tileId)
    },

    focusedTileId(): string {
      return state.focusedTileId ?? firstLeafId(state.root) ?? ''
    },

    initSingleTile(): string {
      const tileId = generateTileId()
      setState(produce((s) => {
        s.root = { type: 'leaf', id: tileId }
        s.focusedTileId = tileId
      }))
      return tileId
    },

    splitTile,

    makeGrid,

    removeGrid,

    replaceGridWithLeaf,

    updateGridRatios(gridId: string, axis: GridAxis, ratios: number[]): boolean {
      let mutated = false
      setState('root', produce((root) => {
        mutated = applyGridRatios(root, gridId, axis, ratios)
      }))
      return mutated
    },

    closeTile(tileId: string) {
      const plan = planCloseTile(state, tileId, generateTileId)
      // The main layout silently swallows the would-be-empty case (closing
      // the only tile in the workspace is a no-op via this path; the
      // workspace-level last-tab flow handles it instead).
      if (plan.kind !== 'changed')
        return
      setState(produce((s) => {
        s.root = plan.root
        s.focusedTileId = plan.focusedTileId
      }))
    },

    updateRatios(splitId: string, ratios: number[]): boolean {
      let mutated = false
      setState('root', produce((root) => {
        mutated = applySplitRatios(root, splitId, ratios)
      }))
      return mutated
    },

    getAllTileIds(): string[] {
      return allTileIdsMemo()
    },

    /** True iff the layout has at least two leaves. */
    hasMultipleTiles(): boolean {
      return hasMultipleTilesMemo()
    },

    owner: () => layoutOwner,

    toProto(): LayoutNode {
      return toProto(state.root)
    },

    fromProto(node: LayoutNode) {
      const local = fromProto(node, generateTileId)
      this.setLayout(local)
    },

    /** Snapshot the current state for registry caching. */
    snapshot(): LayoutStoreState {
      return {
        root: cloneNode(state.root),
        focusedTileId: state.focusedTileId,
      }
    },

    /** Restore from a previously snapshotted state. */
    restore(snap: LayoutStoreState) {
      setState('root', cloneNode(snap.root))
      setState('focusedTileId', snap.focusedTileId)
    },
  }
}

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
