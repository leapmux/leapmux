import type { OpBuilderCtx } from './bridge'
import type { OrgCrdtState } from '~/generated/leapmux/v1/org_crdt_pb'
import type { OpBatch, OrgOp, SetFloatingWindowRegisterOp, SetNodeRegisterOp, SetTabRegisterOp } from '~/generated/leapmux/v1/org_ops_pb'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { customAlphabet } from 'nanoid'
import { DoubleListSchema } from '~/generated/leapmux/v1/org_crdt_pb'
import {
  OpBatchSchema,
  OrgOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneFloatingWindowOpSchema,
  TombstoneNodeOpSchema,
  TombstoneTabOpSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { after, first } from '~/lib/lexorank'
import { hlcIsZero } from './hlc'
import { cmpStr } from './project'

/**
 * generateId mints a 48-character alphanumeric nanoid that matches
 * the Go-side `util/id.Generate()` shape. Used for op_id, batch_id,
 * node_id, tab_id, and window_id wherever the frontend needs a fresh
 * client-minted identifier.
 */
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
const nanoid48 = customAlphabet(ALPHABET, 48)

export function generateId(): string {
  return nanoid48()
}

function buildOp(ctx: OpBuilderCtx, body: OrgOp['body']): OrgOp {
  return create(OrgOpSchema, {
    orgId: ctx.orgId,
    opId: generateId(),
    originClientId: ctx.originClientId,
    clientHlc: ctx.clock.tick(),
    body,
  })
}

/**
 * Builders for each op variant. Returned ops carry an advisory
 * client_hlc; the hub assigns canonical_hlc on commit.
 *
 * Per-register helpers compose around three register-family
 * constructors — `setNodeRegister`, `setTabRegister`, and
 * `setFloatingRegister` — that accept the structurally-typed `field`
 * union from the generated proto types. Adding a new register is now
 * a one-line wrapper instead of a fresh six-line builder.
 */

function setNodeRegister(ctx: OpBuilderCtx, nodeId: string, field: SetNodeRegisterOp['field']): OrgOp {
  return buildOp(ctx, {
    case: 'setNodeRegister',
    value: create(SetNodeRegisterOpSchema, { nodeId, field }),
  })
}

function setTabRegister(ctx: OpBuilderCtx, tabType: TabType, tabId: string, field: SetTabRegisterOp['field']): OrgOp {
  return buildOp(ctx, {
    case: 'setTabRegister',
    value: create(SetTabRegisterOpSchema, { tabType, tabId, field }),
  })
}

function setFloatingRegister(ctx: OpBuilderCtx, windowId: string, field: SetFloatingWindowRegisterOp['field']): OrgOp {
  return buildOp(ctx, {
    case: 'setFloatingWindowRegister',
    value: create(SetFloatingWindowRegisterOpSchema, { windowId, field }),
  })
}

export function setNodeKind(ctx: OpBuilderCtx, nodeId: string, kind: number): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'kind', value: kind })
}

export function setNodeParentId(ctx: OpBuilderCtx, nodeId: string, parentId: string): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'parentId', value: parentId })
}

export function setNodePosition(ctx: OpBuilderCtx, nodeId: string, position: string): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'position', value: position })
}

export function setNodeDirection(ctx: OpBuilderCtx, nodeId: string, direction: number): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'direction', value: direction })
}

export function setNodeRatios(ctx: OpBuilderCtx, nodeId: string, ratios: number[]): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'ratios', value: create(DoubleListSchema, { values: ratios }) })
}

export function setNodeRows(ctx: OpBuilderCtx, nodeId: string, rows: number): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'rows', value: rows })
}

export function setNodeCols(ctx: OpBuilderCtx, nodeId: string, cols: number): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'cols', value: cols })
}

export function setNodeRowRatios(ctx: OpBuilderCtx, nodeId: string, values: number[]): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'rowRatios', value: create(DoubleListSchema, { values }) })
}

export function setNodeColRatios(ctx: OpBuilderCtx, nodeId: string, values: number[]): OrgOp {
  return setNodeRegister(ctx, nodeId, { case: 'colRatios', value: create(DoubleListSchema, { values }) })
}

export function tombstoneNode(ctx: OpBuilderCtx, nodeId: string): OrgOp {
  return buildOp(ctx, {
    case: 'tombstoneNode',
    value: create(TombstoneNodeOpSchema, { nodeId }),
  })
}

export function setTabTileId(ctx: OpBuilderCtx, tabType: TabType, tabId: string, tileId: string): OrgOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'tileId', value: tileId })
}

export function setTabPosition(ctx: OpBuilderCtx, tabType: TabType, tabId: string, position: string): OrgOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'position', value: position })
}

export function setTabWorkerId(ctx: OpBuilderCtx, tabType: TabType, tabId: string, workerId: string): OrgOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'workerId', value: workerId })
}

export function setTabDisplayMode(ctx: OpBuilderCtx, tabType: TabType, tabId: string, mode: number): OrgOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'displayMode', value: mode })
}

export function setTabFileViewMode(ctx: OpBuilderCtx, tabType: TabType, tabId: string, mode: number): OrgOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'fileViewMode', value: mode })
}

export function setTabFileDiffBase(ctx: OpBuilderCtx, tabType: TabType, tabId: string, base: string): OrgOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'fileDiffBase', value: base })
}

export function tombstoneTab(ctx: OpBuilderCtx, tabType: TabType, tabId: string): OrgOp {
  return buildOp(ctx, {
    case: 'tombstoneTab',
    value: create(TombstoneTabOpSchema, { tabType, tabId }),
  })
}

export function setFloatingWorkspaceId(ctx: OpBuilderCtx, windowId: string, workspaceId: string): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'workspaceId', value: workspaceId })
}

export function setFloatingX(ctx: OpBuilderCtx, windowId: string, x: number): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'x', value: x })
}

export function setFloatingY(ctx: OpBuilderCtx, windowId: string, y: number): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'y', value: y })
}

export function setFloatingWidth(ctx: OpBuilderCtx, windowId: string, width: number): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'width', value: width })
}

export function setFloatingHeight(ctx: OpBuilderCtx, windowId: string, height: number): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'height', value: height })
}

export function setFloatingOpacity(ctx: OpBuilderCtx, windowId: string, opacity: number): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'opacity', value: opacity })
}

export function setFloatingRootNodeId(ctx: OpBuilderCtx, windowId: string, rootNodeId: string): OrgOp {
  return setFloatingRegister(ctx, windowId, { case: 'rootNodeId', value: rootNodeId })
}

export function tombstoneFloatingWindow(ctx: OpBuilderCtx, windowId: string): OrgOp {
  return buildOp(ctx, {
    case: 'tombstoneFloatingWindow',
    value: create(TombstoneFloatingWindowOpSchema, { windowId }),
  })
}

/** Bundle a list of ops into a fresh OpBatch. */
export function newBatch(ops: OrgOp[]): OpBatch {
  return create(OpBatchSchema, { batchId: generateId(), ops })
}

/**
 * Enumerate the live (non-tombstoned) tabs anchored to a tile in the
 * given state, sorted by their CURRENT user-visible order (LexoRank
 * position ascending, tab_id as tiebreak). Used by op-builders that
 * migrate or tombstone tabs en masse when their parent tile collapses
 * or moves (`emitMakeGrid`, `buildCloseTileOps`, `tileOps`).
 *
 * The ordering matters: every caller pairs the returned tabs with
 * `lexorankAt(i)` to mint fresh ranks for a destination tile. If we
 * returned tabs in `Object.values(state.tabs)` order (insertion order
 * of the CRDT map, which has nothing to do with what the user sees),
 * the destination tile would end up reordered on every make-grid /
 * close-tile structural change. Sorting here makes the migration
 * order-preserving by construction; the tiebreak matches `tabsByTile`
 * in tab.store so the visible order is identical before and after.
 */
export function liveTabsOnTile(state: OrgCrdtState, tileId: string): Array<{ tabType: TabType, tabId: string }> {
  const matches: Array<{ tabType: TabType, tabId: string, position: string }> = []
  for (const t of Object.values(state.tabs)) {
    if (!hlcIsZero(t.tombstoneAt))
      continue
    if ((t.tileId?.value ?? '') === tileId)
      matches.push({ tabType: t.tabType, tabId: t.tabId, position: t.position?.value ?? '' })
  }
  matches.sort((a, b) => {
    if (a.position !== b.position)
      return a.position < b.position ? -1 : 1
    return cmpStr(a.tabId, b.tabId)
  })
  return matches.map(t => ({ tabType: t.tabType, tabId: t.tabId }))
}

/** Returns a uniform-ratio array of length n (e.g. [0.5, 0.5] for n=2). */
export function equalRatios(n: number): number[] {
  if (n <= 0)
    return []
  const ratio = 1 / n
  return (Array.from({ length: n }) as number[]).fill(ratio)
}

/**
 * Stable LexoRank for a synthetic insertion at index `i` — used when
 * migrating tabs en masse to a fresh tile where cross-tile order
 * doesn't matter, only that each tab gets a unique well-formed rank.
 * `mid(cur, '')` is by definition `after(cur)`; the direct call saves
 * an empty-arg branch on every iteration.
 */
export function lexorankAt(i: number): string {
  let cur = first()
  for (let k = 0; k < i; k++)
    cur = after(cur)
  return cur
}
