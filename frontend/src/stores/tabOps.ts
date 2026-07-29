import type { Tab } from './tab.types'
import type { CrdtOp } from '~/generated/leapmux/v1/user_ops_pb'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import type { OpBuilderCtx } from '~/lib/crdt'
import {
  ctxFromBridge,
  getCRDTBridge,
  newBatch,
  setTabPosition,
  setTabTileId,
  setTabWorkerId,
  tombstoneTab,
} from '~/lib/crdt'
import { after, first, positionAtInsertIdx } from '~/lib/lexorank'
import { tabKey } from './tab.helpers'

/**
 * The only write path for anything the CRDT carries.
 *
 * Nothing here touches local state. `tile_id`, `position`, `worker_id` and the
 * FILE view registers live in the CRDT and are read back through the projection,
 * so a mutation is an op and nothing else — the change becomes visible because
 * `PendingOpsManager` applies the batch to `speculativeState` synchronously on
 * enqueue, not because a store was written alongside it.
 *
 * That is what retires `{ silent: true }`. It existed to suppress emission for
 * writes that came *from* the projection; with no local copy there are no such
 * writes, and a silent mutation and a real one can no longer look identical at
 * the call site.
 *
 * Mirrors `layoutOps.ts` / `floatingWindowOps.ts`, which already had this shape.
 */

/**
 * Resolve the bridge, build ops, enqueue as one batch. Returns the batch id so
 * callers like the cross-workspace move can correlate a later `BatchResult`, or
 * null when the bridge is unwired (tests) or the builder produced nothing.
 */
function emitOps(build: (ctx: OpBuilderCtx) => CrdtOp[]): string | null {
  const bridge = getCRDTBridge()
  if (!bridge)
    return null
  const ops = build(ctxFromBridge(bridge))
  if (ops.length === 0)
    return null
  return bridge.enqueue(newBatch(ops))
}

/**
 * Place a new tab: tile, position, and worker when known. `position` is
 * computed by the caller from the tabs already on the tile.
 */
export function emitAddTab(tab: {
  type: TabType
  id: string
  tileId: string
  position: string
  workerId?: string
}): string | null {
  return emitOps((ctx) => {
    const ops = [
      setTabTileId(ctx, tab.type, tab.id, tab.tileId),
      setTabPosition(ctx, tab.type, tab.id, tab.position),
    ]
    if (tab.workerId)
      ops.push(setTabWorkerId(ctx, tab.type, tab.id, tab.workerId))
    return ops
  })
}

export function emitRemoveTab(type: TabType, id: string): string | null {
  return emitOps(ctx => [tombstoneTab(ctx, type, id)])
}

export function emitSetTabPosition(type: TabType, id: string, position: string): string | null {
  return emitOps(ctx => [setTabPosition(ctx, type, id, position)])
}

/**
 * Move a tab to another tile.
 *
 * No-op when the tab is already there. Drops fire constantly on the tile a tab
 * already occupies (any drag that ends where it started, plus every
 * `moveTabToTile` the detach/attach flows call defensively), and each redundant
 * emit is a batch the hub must accept and every peer must apply. Reads the
 * current tile from `speculativeState` rather than a local copy, so a move that
 * is still un-acked counts as having happened.
 */
export function emitMoveTabToTile(type: TabType, id: string, tileId: string): string | null {
  const current = getCRDTBridge()?.speculativeState()?.tabs[id]?.tileId?.value
  if (current === tileId)
    return null
  return emitOps(ctx => [setTabTileId(ctx, type, id, tileId)])
}

/**
 * Reorder within a tile: compute the dragged tab's new LexoRank and emit it.
 *
 * Swap-on-cross semantics — the dragged tab ends up on the far side of the
 * target relative to where it came from. With `moved` spliced out, inserting at
 * `toIdx` puts it after the target when moving forward (the target shifted left)
 * and before it when moving backward. "Always insert before target" is symmetric
 * on paper but degenerates to a no-op when dragging onto the right neighbour.
 *
 * Returns the BATCH ID -- like every sibling emitter here -- or null when the
 * drag was a no-op. Deliberately not the computed rank: both are `string | null`,
 * so returning the rank would type-check at a caller that registers a
 * `batchResultHandlers` entry from it (as `useCrossWorkspaceMove` does with the
 * others) and key that map on a LexoRank, whose rollback could then never fire.
 */
export function emitReorderTabs(tabsInTile: Tab[], fromKey: string, toKey: string): string | null {
  const fromIdx = tabsInTile.findIndex(t => tabKey(t) === fromKey)
  const toIdx = tabsInTile.findIndex(t => tabKey(t) === toKey)
  if (fromIdx === -1 || toIdx === -1 || fromIdx === toIdx)
    return null
  const reordered = tabsInTile.map(t => ({ ...t }))
  const [moved] = reordered.splice(fromIdx, 1)
  const newPosition = positionAtInsertIdx(reordered, toIdx)
  return emitSetTabPosition(moved.type, moved.id, newPosition)
}

/**
 * Reassign every tab on `oldTileIds` to `newTileId` in one batch. Used by the
 * "convert grid to tile" close mode. One batch keeps the wire bounded and
 * preserves atomicity at the hub.
 */
export function emitReassignTabsToTile(tabs: Tab[], oldTileIds: string[], newTileId: string): string | null {
  const oldSet = new Set(oldTileIds)
  const reassigned = tabs.filter(t => t.tileId !== undefined && oldSet.has(t.tileId) && t.tileId !== newTileId)
  if (reassigned.length === 0)
    return null
  return emitOps(ctx => reassigned.map(t => setTabTileId(ctx, t.type, t.id, newTileId)))
}

/**
 * Move every tab from `sourceTileId` onto the end of `targetTileId`. Emits
 * tile_id + position per tab in one batch; the pair is co-scheduled by the
 * OpsSubmitter's aggregator and validated together at the hub.
 */
export function emitMergeTabsIntoTile(
  sourceTabs: Tab[],
  targetTabs: Tab[],
  targetTileId: string,
): string | null {
  if (sourceTabs.length === 0)
    return null
  let lastPos = targetTabs.at(-1)?.position ?? ''
  const moves: { type: TabType, id: string, position: string }[] = []
  for (const tab of sourceTabs) {
    const newPos = lastPos ? after(lastPos) : first()
    lastPos = newPos
    moves.push({ type: tab.type, id: tab.id, position: newPos })
  }
  return emitOps((ctx) => {
    const ops: CrdtOp[] = []
    for (const m of moves) {
      ops.push(setTabTileId(ctx, m.type, m.id, targetTileId))
      ops.push(setTabPosition(ctx, m.type, m.id, m.position))
    }
    return ops
  })
}

/**
 * Move a tab into a tile in a different workspace.
 *
 * Crucially NOT remove-from-source + add-to-target: `TombstoneTab` is
 * remove-wins, so a later `SetTabRegister` on the same tab_id would be silently
 * dropped at the hub. The single-LWW-write pattern lets one tab record cross the
 * workspace boundary atomically — the owning workspace is derived from the new
 * tile's ancestor chain, and `worker_id` does not change.
 *
 * `tileId` must be a leaf in the destination workspace's tree; callers resolve
 * it from the projection.
 */
export function emitMoveTabToWorkspace(
  type: TabType,
  id: string,
  tileId: string,
  position: string,
): string | null {
  return emitOps(ctx => [
    setTabTileId(ctx, type, id, tileId),
    setTabPosition(ctx, type, id, position),
  ])
}

/** Position for a tab appended to the end of a tile's existing tabs. */
export function positionAtEnd(tabsInTile: Tab[]): string {
  return positionAtInsertIdx(tabsInTile, tabsInTile.length)
}

/** Position for a tab inserted directly after `afterKey` within a tile. */
export function positionAfterKey(tabsInTile: Tab[], afterKey: string | undefined): string {
  if (!afterKey)
    return positionAtEnd(tabsInTile)
  const idx = tabsInTile.findIndex(t => tabKey(t) === afterKey)
  return positionAtInsertIdx(tabsInTile, idx >= 0 ? idx + 1 : tabsInTile.length)
}
