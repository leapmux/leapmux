import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import type { CRDTBridge } from '~/lib/crdt'
import { NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import {
  ctxFromBridge,
  generateId,
  hlcIsZero,
  newBatch,
  parentOf,
  setFloatingHeight,
  setFloatingOpacity,
  setFloatingRootNodeId,
  setFloatingWidth,
  setFloatingWorkspaceId,
  setFloatingX,
  setFloatingY,
  setNodeKind,
  tombstoneFloatingWindow,
  withBridgeAndState,
} from '~/lib/crdt'
import { buildCloseSubtreeOps, buildCloseTileOps, buildReplaceNonRootGridWithLeafOps } from './tileOps'

/**
 * floatingWindowOps owns the CRDT op-batch builders for floating-
 * window mutations. Mirrors the layout-store helpers but addresses
 * the FloatingWindowRecord plus its inner tree (rooted at
 * `FloatingWindowRecord.root_node_id`).
 *
 * Plan-aligned design:
 *  - addWindow: a single creation batch — SetNodeRegister(rootId,
 *    kind=LEAF), then SetFloatingWindowRegister(windowId,
 *    root_node_id=rootId), then workspace_id and geometry/opacity
 *    registers. The validator's paired-creation rule allows the new
 *    root node's `parent_id=""` because the same batch creates the
 *    window referencing it.
 *  - removeWindow / closeTile (last tile): recursive tombstones of
 *    every descendant leaves-first, then the root, then the window.
 *  - updatePosition / updateGeometry / updateOpacity: single-op
 *    register writes.
 *  - splitTile / makeGrid / removeGrid / replaceGridWithLeaf inside
 *    a window mirror their main-tree counterparts.
 *
 * The floating-window store treats z-order (state.windows array
 * order) as purely local — z-index isn't a CRDT register. Same for
 * focus inside a window.
 */

/**
 * Open a fresh floating window. Submits one batch creating the root
 * node and every window register. Returns the new ids so the caller
 * can immediately address the freshly-created tile (e.g. open a tab
 * inside it).
 */
export function emitAddFloatingWindow(
  bridge: CRDTBridge,
  geometry: { x: number, y: number, width: number, height: number, opacity?: number },
): { windowId: string, rootTileId: string } | null {
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return null
  const wsId = bridge.workspaceId()
  if (!wsId)
    return null
  const windowId = generateId()
  const rootTileId = generateId()
  const opacity = geometry.opacity ?? 1
  const ops: OrgOp[] = [
    // Root node first (kind=LEAF; parent_id stays "" by default —
    // the validator's paired-creation rule allows this because the
    // same batch creates a window referencing it).
    setNodeKind(ctx, rootTileId, NodeKind.LEAF),
    // Then the window registers in plan order.
    setFloatingRootNodeId(ctx, windowId, rootTileId),
    setFloatingWorkspaceId(ctx, windowId, wsId),
    setFloatingX(ctx, windowId, geometry.x),
    setFloatingY(ctx, windowId, geometry.y),
    setFloatingWidth(ctx, windowId, geometry.width),
    setFloatingHeight(ctx, windowId, geometry.height),
    setFloatingOpacity(ctx, windowId, opacity),
  ]
  bridge.enqueue(newBatch(ops))
  return { windowId, rootTileId }
}

/**
 * Close (tombstone) a floating window plus its entire inner subtree.
 * Tombstones every live tab in the subtree, then every non-root
 * descendant leaves-first, then the root, then the window.
 */
export function emitRemoveFloatingWindow(bridge: CRDTBridge, windowId: string): void {
  withBridgeAndState(bridge, (ctx, state) => {
    const fw = state.floatingWindows[windowId]
    if (!fw || !hlcIsZero(fw.tombstoneAt))
      return
    const rootId = fw.rootNodeId
    const ops: OrgOp[] = rootId !== ''
      ? buildCloseSubtreeOps(ctx, state, rootId, { tombstoneRoot: true })
      : []
    ops.push(tombstoneFloatingWindow(ctx, windowId))
    bridge.enqueue(newBatch(ops))
  }, undefined as void)
}

/** Single-op write: window x register. */
export function emitUpdatePosition(bridge: CRDTBridge, windowId: string, x: number, y: number): void {
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return
  bridge.enqueue(newBatch([
    setFloatingX(ctx, windowId, x),
    setFloatingY(ctx, windowId, y),
  ]))
}

/** Position + size in one batch — used by drag-resize pointermove. */
export function emitUpdateGeometry(
  bridge: CRDTBridge,
  windowId: string,
  x: number,
  y: number,
  width: number,
  height: number,
): void {
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return
  bridge.enqueue(newBatch([
    setFloatingX(ctx, windowId, x),
    setFloatingY(ctx, windowId, y),
    setFloatingWidth(ctx, windowId, width),
    setFloatingHeight(ctx, windowId, height),
  ]))
}

/** Single-op write: window opacity register. */
export function emitUpdateOpacity(bridge: CRDTBridge, windowId: string, opacity: number): void {
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return
  bridge.enqueue(newBatch([setFloatingOpacity(ctx, windowId, opacity)]))
}

/**
 * Inner-tree close-tile. Shares `buildCloseTileOps` with the main
 * tree's `emitCloseTile` so both get the same undo-split rewiring —
 * see `tileOps.ts` for the rationale. If the tile is the window's
 * root, the caller must use `emitRemoveFloatingWindow` instead — the
 * validator rejects `root_node_protected` for direct root tombstones.
 */
export function emitFwCloseTile(bridge: CRDTBridge, tileId: string): void {
  withBridgeAndState(bridge, (ctx, state) => {
    bridge.enqueue(newBatch(buildCloseTileOps(ctx, state, tileId)))
  }, undefined as void)
}

/** removeGrid on an inner-tree grid. */
export function emitFwRemoveGrid(bridge: CRDTBridge, gridId: string): void {
  withBridgeAndState(bridge, (ctx, state) => {
    bridge.enqueue(newBatch(buildCloseSubtreeOps(ctx, state, gridId, { tombstoneRoot: true })))
  }, undefined as void)
}

/** replaceGridWithLeaf on an inner-tree grid. */
export function emitFwReplaceGridWithLeaf(bridge: CRDTBridge, gridId: string): string | null {
  return withBridgeAndState(bridge, (ctx, state) => {
    const parentId = parentOf(state, gridId)
    if (parentId === '')
      return null
    const { ops, newLeafId } = buildReplaceNonRootGridWithLeafOps(ctx, state, gridId, parentId)
    bridge.enqueue(newBatch(ops))
    return newLeafId
  }, null)
}
