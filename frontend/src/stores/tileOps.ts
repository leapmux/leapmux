import type { NodeRecord, OrgCrdtState } from '~/generated/leapmux/v1/org_crdt_pb'
import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import type { OpBuilderCtx } from '~/lib/crdt'
import { NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import {
  buildChildIndex,
  descendantsLeavesFirst,
  generateId,
  hlcIsZero,
  lexorankAt,
  liveTabsOnTile,
  setNodeKind,
  setNodeParentId,
  setNodePosition,
  setTabPosition,
  setTabTileId,
  tombstoneNode,
  tombstoneTab,
} from '~/lib/crdt'
import { first } from '~/lib/lexorank'

/**
 * tileOps holds the shared op-builder primitives used by both the
 * main-tree (`layoutOps.ts`) and floating-window (`floatingWindowOps.
 * ts`) close/remove/replace flows. The two trees share the same
 * projection rules (single-child SPLIT collapse, leaves-first
 * tombstone order, root-protected nodes) so the rewrite logic must
 * match — putting the builders here keeps them from drifting.
 *
 * None of these enqueue anything; they return `OrgOp[]` so callers
 * can compose with their own pre/post ops (e.g. floating-window
 * creation around `buildCloseSubtreeOps`).
 */

/**
 * buildCloseTileOps produces the ops to close a single tile: tombstone
 * every live tab on it, tombstone the tile, and — for tiles whose
 * parent is a SPLIT with exactly 2 live children — collapse the
 * parent back to a LEAF and migrate the sibling's tabs to the parent.
 *
 * Why the undo-split: `project.ts:buildTree` collapses a SPLIT with
 * exactly one live child to that child, re-keying the rendered leaf
 * to the parent's node_id. The sibling's tabs are stored with
 * tile_id=sibling, so they orphan in the rendered tree. The user sees
 * the surviving tile render empty even though the sidebar still
 * lists the tabs. Completing the inverse-split inside the same batch
 * keeps the rendered tree consistent.
 *
 * Caller is responsible for NOT calling this on a registered root
 * (workspace root or floating-window root). The validator rejects
 * `root_node_protected` for direct root tombstones and the whole batch
 * rolls back.
 */
export function buildCloseTileOps(
  ctx: OpBuilderCtx,
  state: OrgCrdtState,
  tileId: string,
  childIndex?: Map<string, NodeRecord[]>,
): OrgOp[] {
  const ops: OrgOp[] = []
  for (const t of liveTabsOnTile(state, tileId))
    ops.push(tombstoneTab(ctx, t.tabType, t.tabId))
  ops.push(tombstoneNode(ctx, tileId))

  const closingNode = state.nodes[tileId]
  const parentId = closingNode?.parentId ?? ''
  if (!parentId)
    return ops
  const parent = state.nodes[parentId]
  if (!parent || parent.kind?.value !== NodeKind.SPLIT || !hlcIsZero(parent.tombstoneAt))
    return ops

  // `buildChildIndex` already drops tombstoned nodes, so an O(1)
  // `idx.get(parentId)` replaces the O(N nodes) `Object.values`
  // walk the previous implementation paid. Callers that have a
  // precomputed index (e.g. rendering many subtrees from the same
  // state) thread it in to share the single O(N) pass.
  const idx = childIndex ?? buildChildIndex(state)
  const liveChildren = (idx.get(parentId) ?? []).map(n => n.nodeId)
  if (liveChildren.length !== 2 || !liveChildren.includes(tileId))
    return ops

  const siblingId = liveChildren.find(id => id !== tileId)!
  // Inverse-split only fires when the sibling is itself a leaf. If the
  // sibling is a SPLIT or GRID, tombstoning it would orphan every
  // descendant tile + every tab under those descendants — the
  // validator then rejects the batch with
  // BATCH_REJECTION_TAB_PLACEMENT_INVALID because the surviving tabs
  // reference a now-dead tile chain.
  //
  // For the non-leaf-sibling case the rendered tree's single-child
  // SPLIT collapse (just above in this file's sibling `project.ts`)
  // already does the right thing: the surviving sub-tree's root
  // re-keys to the parent's id and its descendants render as before.
  // So we don't need any rewiring; just the closing-leaf tombstone is
  // enough.
  const sibling = state.nodes[siblingId]
  const sibKind = sibling?.kind?.value ?? NodeKind.LEAF
  if (sibKind !== NodeKind.LEAF)
    return ops
  const sibTabs = liveTabsOnTile(state, siblingId)
  for (let i = 0; i < sibTabs.length; i++) {
    const t = sibTabs[i]
    ops.push(setTabTileId(ctx, t.tabType, t.tabId, parentId))
    ops.push(setTabPosition(ctx, t.tabType, t.tabId, lexorankAt(i)))
  }
  ops.push(tombstoneNode(ctx, siblingId))
  ops.push(setNodeKind(ctx, parentId, NodeKind.LEAF))
  return ops
}

/**
 * Options for `buildCloseSubtreeOps`.
 */
export interface CloseSubtreeOpts {
  /**
   * When set, every live tab in the subtree is migrated to this tile
   * id (via SetTabRegister(tile_id=migrateTabsTo)) instead of being
   * tombstoned. Used by replaceGridWithLeaf so tabs survive the grid
   * collapse.
   */
  migrateTabsTo?: string
  /**
   * When false, the root tile (`tileId`) is NOT tombstoned — the
   * caller is responsible for handling it (e.g. flipping it to LEAF
   * in the root-grid case, or tombstoning a floating window record
   * separately). Defaults to true.
   */
  tombstoneRoot?: boolean
}

/**
 * buildCloseSubtreeOps walks the descendants of `tileId` leaves-
 * first and produces the ops to either tombstone or migrate every
 * live tab + non-root node in the subtree. The root (`tileId`) is
 * handled per `opts.tombstoneRoot`.
 *
 * Used by removeGrid / replaceGridWithLeaf (main + floating-window
 * variants) and the floating-window removal path.
 */
export function buildCloseSubtreeOps(
  ctx: OpBuilderCtx,
  state: OrgCrdtState,
  tileId: string,
  opts: CloseSubtreeOpts = {},
  childIndex?: Map<string, NodeRecord[]>,
): OrgOp[] {
  const migrateTo = opts.migrateTabsTo
  const tombstoneRoot = opts.tombstoneRoot !== false
  const descendants = descendantsLeavesFirst(state, tileId, childIndex)
  const ops: OrgOp[] = []
  // Migrate-or-tombstone tabs walk leaves-first to keep the wire
  // order stable and the validator's intermediate states predictable.
  let migratedPos = 0
  for (const id of descendants) {
    if (id === tileId)
      continue
    for (const t of liveTabsOnTile(state, id)) {
      if (migrateTo !== undefined) {
        ops.push(setTabTileId(ctx, t.tabType, t.tabId, migrateTo))
        ops.push(setTabPosition(ctx, t.tabType, t.tabId, lexorankAt(migratedPos++)))
      }
      else {
        ops.push(tombstoneTab(ctx, t.tabType, t.tabId))
      }
    }
    ops.push(tombstoneNode(ctx, id))
  }
  // Root tile's tabs.
  for (const t of liveTabsOnTile(state, tileId)) {
    if (migrateTo !== undefined) {
      ops.push(setTabTileId(ctx, t.tabType, t.tabId, migrateTo))
      ops.push(setTabPosition(ctx, t.tabType, t.tabId, lexorankAt(migratedPos++)))
    }
    else {
      ops.push(tombstoneTab(ctx, t.tabType, t.tabId))
    }
  }
  if (tombstoneRoot)
    ops.push(tombstoneNode(ctx, tileId))
  return ops
}

/**
 * buildReplaceNonRootGridWithLeafOps tombstones a non-root GRID/SPLIT
 * + every descendant and creates a fresh LEAF in the grid's slot under
 * `parentId`, migrating any live tabs from the closed subtree onto
 * that new leaf. The new leaf inherits the closed grid's LexoRank
 * position so the renderer keeps the same slot.
 *
 * Non-root only: a root grid has no parent slot to inherit and must be
 * collapsed in place (root_node_id is set-once, so the root NodeRecord
 * stays alive and only its kind flips to LEAF). Callers that may face
 * either case keep their own root branch and delegate the non-root
 * path here.
 *
 * Returns the new leaf id alongside the op list. The caller is
 * responsible for wrapping it in `newBatch` and enqueuing on the
 * bridge.
 */
export function buildReplaceNonRootGridWithLeafOps(
  ctx: OpBuilderCtx,
  state: OrgCrdtState,
  gridId: string,
  parentId: string,
  childIndex?: Map<string, NodeRecord[]>,
): { ops: OrgOp[], newLeafId: string } {
  const newLeafId = generateId()
  const gridRec = state.nodes[gridId]
  const inheritedPosition = gridRec?.position?.value ?? first()
  const ops = buildCloseSubtreeOps(ctx, state, gridId, { migrateTabsTo: newLeafId, tombstoneRoot: true }, childIndex)
  ops.push(setNodeKind(ctx, newLeafId, NodeKind.LEAF))
  ops.push(setNodeParentId(ctx, newLeafId, parentId))
  ops.push(setNodePosition(ctx, newLeafId, inheritedPosition))
  return { ops, newLeafId }
}
