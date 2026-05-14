import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import type { CRDTBridge } from '~/lib/crdt'
import { NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import { SplitDirection } from '~/generated/leapmux/v1/workspace_pb'
import {
  ctxFromBridge,
  equalRatios,
  generateId,
  lexorankAt,
  liveTabsOnTile,
  newBatch,
  parentOf,
  setNodeColRatios,
  setNodeCols,
  setNodeDirection,
  setNodeKind,
  setNodeParentId,
  setNodePosition,
  setNodeRatios,
  setNodeRowRatios,
  setNodeRows,
  setTabPosition,
  setTabTileId,
  withBridgeAndState,
} from '~/lib/crdt'
import { after, first } from '~/lib/lexorank'
import { buildCloseSubtreeOps, buildCloseTileOps, buildReplaceNonRootGridWithLeafOps } from './tileOps'

/**
 * layoutOps owns the CRDT op-batch builders for every layout-store
 * mutation. Each function reads the bridge's speculative state to
 * resolve current parent_ids / sibling positions / live tabs, mints
 * fresh node ids where needed, and submits one OpBatch via
 * `bridge.enqueue`.
 *
 * The store is purely projection-derived — no local tree mutation
 * happens here, only op submission. The hub re-broadcasts canonical-
 * HLC-tagged ops; the local PendingOpsManager folds them into
 * speculativeState; the projection re-renders.
 *
 * **Plan-aligned design notes**:
 *  - splitTile flips T's kind LEAF → SPLIT in place (set-once
 *    parent_id on T is preserved). Two new leaf children A, B are
 *    created with parent_id=T. Tabs on T migrate to A.
 *  - makeGrid flips T's kind LEAF → GRID, with R*C new leaf cells,
 *    parent_id=T. Tabs on T migrate to cell[0,0].
 *  - closeTile (non-root child) tombstones the leaf and every tab
 *    on it. The parent's projection collapses to a single child if
 *    appropriate; the next user mutation can in-place collapse.
 *  - removeGrid + replaceGridWithLeaf walk the descendants leaves-
 *    first and tombstone each, then handle the grid itself per the
 *    operation's contract.
 *  - updateRatios / updateGridRatios are single-op writes.
 */

/**
 * Emit a split-tile op batch. Returns the new sibling tile id (B) so
 * callers can target it (e.g. open a tab on the freshly-split tile).
 *
 * Contract: T must currently be a LEAF in the projected tree. The
 * validator enforces that constraint; this helper doesn't pre-check
 * because the rejection path already surfaces a useful toast.
 */
export function emitSplitTile(
  bridge: CRDTBridge,
  parentTileId: string,
  direction: 'horizontal' | 'vertical',
): { childA: string, childB: string } | null {
  return withBridgeAndState(bridge, (ctx, state) => {
    const tabs = liveTabsOnTile(state, parentTileId)
    const childA = generateId()
    const childB = generateId()
    const dir = direction === 'horizontal' ? SplitDirection.HORIZONTAL : SplitDirection.VERTICAL
    const posA = first()
    const posB = after(posA)
    const ops: OrgOp[] = [
      setNodeKind(ctx, parentTileId, NodeKind.SPLIT),
      setNodeDirection(ctx, parentTileId, dir),
      setNodeRatios(ctx, parentTileId, [0.5, 0.5]),
      setNodeKind(ctx, childA, NodeKind.LEAF),
      setNodeParentId(ctx, childA, parentTileId),
      setNodePosition(ctx, childA, posA),
      setNodeKind(ctx, childB, NodeKind.LEAF),
      setNodeParentId(ctx, childB, parentTileId),
      setNodePosition(ctx, childB, posB),
      // Tab migrations: tabs on T move to childA (the "where original
      // tabs land" slot). Each tab also re-stamps its position so the
      // ordering is well-defined under LexoRank.
      ...tabs.flatMap((t, i) => [
        setTabTileId(ctx, t.tabType, t.tabId, childA),
        setTabPosition(ctx, t.tabType, t.tabId, lexorankAt(i)),
      ]),
    ]
    bridge.enqueue(newBatch(ops))
    return { childA, childB }
  }, null)
}

/**
 * Emit a make-grid op batch. T flips LEAF → GRID with `rows × cols`
 * new leaf cells; tabs on T migrate to cell[0,0]. Returns the new
 * cell ids in row-major order so callers can target a specific cell.
 */
export function emitMakeGrid(
  bridge: CRDTBridge,
  parentTileId: string,
  rows: number,
  cols: number,
): { gridId: string, cellTileIds: string[] } | null {
  return withBridgeAndState(bridge, (ctx, state) => {
    const tabs = liveTabsOnTile(state, parentTileId)
    const cellIds: string[] = []
    const ops: OrgOp[] = [
      setNodeKind(ctx, parentTileId, NodeKind.GRID),
      setNodeRows(ctx, parentTileId, rows),
      setNodeCols(ctx, parentTileId, cols),
      setNodeRowRatios(ctx, parentTileId, equalRatios(rows)),
      setNodeColRatios(ctx, parentTileId, equalRatios(cols)),
    ]
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const cellId = generateId()
        cellIds.push(cellId)
        ops.push(setNodeKind(ctx, cellId, NodeKind.LEAF))
        ops.push(setNodeParentId(ctx, cellId, parentTileId))
        ops.push(setNodePosition(ctx, cellId, `${r},${c}`))
      }
    }
    // Migrate tabs to cell[0,0].
    const dest = cellIds[0]
    if (dest) {
      for (let i = 0; i < tabs.length; i++) {
        const t = tabs[i]
        ops.push(setTabTileId(ctx, t.tabType, t.tabId, dest))
        ops.push(setTabPosition(ctx, t.tabType, t.tabId, lexorankAt(i)))
      }
    }
    bridge.enqueue(newBatch(ops))
    return { gridId: parentTileId, cellTileIds: cellIds }
  }, null)
}

/**
 * Emit a close-tile op batch. Tombstones every live tab on the tile
 * and the tile itself; if the parent is a 2-child SPLIT, migrates the
 * sibling's tabs to the parent and collapses the parent back to LEAF
 * (see `buildCloseTileOps` for the undo-split rationale).
 *
 * No-op when called on a workspace root (the validator rejects
 * `root_node_protected`); the caller is responsible for not
 * dispatching close on a root tile.
 */
export function emitCloseTile(bridge: CRDTBridge, tileId: string): void {
  withBridgeAndState(bridge, (ctx, state) => {
    bridge.enqueue(newBatch(buildCloseTileOps(ctx, state, tileId)))
  }, undefined as void)
}

/**
 * Emit an update-ratios op batch — single SetNodeRegister on the
 * SPLIT node's ratios slot. Returns true iff the bridge accepted the
 * batch (the bridge always accepts; false here means "no bridge").
 */
export function emitUpdateRatios(bridge: CRDTBridge, splitId: string, ratios: number[]): boolean {
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return false
  bridge.enqueue(newBatch([setNodeRatios(ctx, splitId, ratios)]))
  return true
}

/**
 * Emit an update-grid-ratios op batch — single SetNodeRegister on
 * either rowRatios or colRatios. Single-op atomic; the validator
 * normalizes -0.0 etc. inside Apply.
 */
export function emitUpdateGridRatios(
  bridge: CRDTBridge,
  gridId: string,
  axis: 'row' | 'col',
  ratios: number[],
): boolean {
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return false
  const op = axis === 'row'
    ? setNodeRowRatios(ctx, gridId, ratios)
    : setNodeColRatios(ctx, gridId, ratios)
  bridge.enqueue(newBatch([op]))
  return true
}

/**
 * Emit a remove-grid op batch. The grid's entire subtree is
 * tombstoned (cells leaves-first, then the grid itself). All tabs
 * inside the subtree are tombstoned too — the close-tile contract
 * applies recursively.
 *
 * Root-grid case: the workspace's root NodeRecord can't be
 * tombstoned (the hub rejects `root_node_protected` and the whole
 * batch rolls back — leaving the user with an empty grid they can't
 * close, because every subsequent attempt hits the same rejection).
 * Mirror `emitReplaceGridWithLeaf`'s root branch instead: tombstone
 * the descendant cells (and any tabs that survived the close-flow's
 * tab-by-tab pass) and flip the root's kind back to LEAF in place.
 * After this the workspace tree is a bare root LEAF.
 */
export function emitRemoveGrid(bridge: CRDTBridge, gridId: string): void {
  withBridgeAndState(bridge, (ctx, state) => {
    const isRoot = parentOf(state, gridId) === ''
    // Walk leaves-first so tabs and inner cells tombstone before their
    // ancestors. For the root case, leave the root NodeRecord alive
    // and flip its kind back to LEAF instead of tombstoning.
    const ops = buildCloseSubtreeOps(ctx, state, gridId, { tombstoneRoot: !isRoot })
    if (isRoot)
      ops.push(setNodeKind(ctx, gridId, NodeKind.LEAF))
    bridge.enqueue(newBatch(ops))
  }, undefined as void)
}

/**
 * Emit a replace-grid-with-leaf op batch. The grid's entire subtree
 * is tombstoned (cells + their tabs); a fresh leaf is created in the
 * grid's slot under the grid's parent. The grid's NodeRecord itself
 * is tombstoned. Returns the new leaf's id.
 *
 * The new leaf inherits the grid's position register so the
 * projection slots it where the grid used to render; the parent's
 * `kind` doesn't change (still SPLIT or GRID, or null for a root).
 *
 * Root-grid case: when the grid is a workspace / floating-window
 * root, the operation is a no-op (the validator rejects
 * `root_node_protected`). Callers should handle root grids via the
 * floating-window-close path or skip the operation.
 */
export function emitReplaceGridWithLeaf(bridge: CRDTBridge, gridId: string): string | null {
  return withBridgeAndState(bridge, (ctx, state) => {
    const parentId = parentOf(state, gridId)

    if (parentId === '') {
      // Root grid: mutate kind in place per plan §"Close (collapse) a
      // SPLIT/GRID T back to a LEAF" rule 4 — root_node_id is set-once
      // and the root NodeRecord stays alive; only the kind changes. Tabs
      // migrate to the now-LEAF node (gridId) itself.
      const ops = buildCloseSubtreeOps(ctx, state, gridId, { migrateTabsTo: gridId, tombstoneRoot: false })
      ops.push(setNodeKind(ctx, gridId, NodeKind.LEAF))
      bridge.enqueue(newBatch(ops))
      return gridId
    }

    // Non-root grid: tombstone the grid + every descendant and create a
    // fresh leaf in the grid's slot under the parent. Migrate tabs to
    // the new leaf in the same batch — the hub's pre-apply tombstone
    // check is satisfied because the tombstones target tiles, not tabs,
    // and the SetTabRegister winds up applied alongside the descendant
    // tombstones with the new leaf created at the end.
    const { ops, newLeafId } = buildReplaceNonRootGridWithLeafOps(ctx, state, gridId, parentId)
    bridge.enqueue(newBatch(ops))
    return newLeafId
  }, null)
}
