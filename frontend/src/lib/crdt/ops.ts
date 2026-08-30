import type { OpBuilderCtx } from './bridge'
import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { CrdtOp, OpBatch, SetFloatingWindowRegisterOp, SetNodeRegisterOp, SetTabRegisterOp } from '~/generated/proto/leapmux/v1/user_ops_pb'
import type { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { customAlphabet } from 'nanoid'
import { DoubleListSchema } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import {
  CrdtOpSchema,
  OpBatchSchema,
  ReviveTabOpSchema,
  SetFloatingWindowRegisterOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneFloatingWindowOpSchema,
  TombstoneNodeOpSchema,
  TombstoneTabOpSchema,
} from '~/generated/proto/leapmux/v1/user_ops_pb'
import { after, first } from '~/lib/lexorank'
import { hlcIsZero } from './hlc'
import { cmpStr } from './project'

/**
 * The entity kinds an op can target, spelled as the `kind` half of the
 * `kind:id` keys the checkpoint chunks and the tombstone pin both use.
 */
export type OpEntityKind = 'node' | 'tab' | 'fw' | 'ws'

/**
 * What `opTarget` found. THREE outcomes, not two -- collapsing the last into
 * the middle is what made the tombstone-pin and the checkpoint dirty-set
 * disagree about a future op kind:
 *
 *  - `entity` -- this op installs into `kind`'s map under `id`.
 *  - `none`   -- this op provably installs nothing (an empty body; `applyOp`
 *                falls through its switch).
 *  - `unknown` -- this build does not recognize the body. Callers MUST degrade
 *                conservatively in their own direction; see `opTarget`.
 */
export type OpTarget
  = | { case: 'entity', kind: OpEntityKind, id: string }
    | { case: 'none' }
    | { case: 'unknown' }

/**
 * The entity an op targets. The SINGLE source of the op -> entity mapping on
 * the client, mirroring the hub's `crdt.OpTarget` (`backend/internal/hub/crdt/
 * op.go`), which likewise lives beside the op definitions rather than with any
 * one consumer.
 *
 * It had been copied into two consumers that then drifted in the dangerous
 * direction: the checkpoint's dirty-set treated an unrecognized body as "assume
 * everything changed" (fail-safe), while the tombstone pin treated it as "this
 * op targets nothing" (fail-OPEN) -- so a newly added op kind would leave its
 * entity unpinned, `pruneTombstonesAtOrBelow` would drop the shell that is the
 * only thing suppressing a later write, and `recomputeSpeculative` would
 * resurrect the entity as a live record. One mapping, and an `unknown` the
 * callers cannot ignore, is what keeps that from coming back.
 *
 * The `default` arm below is a COMPILE-TIME exhaustiveness check: adding a case
 * to `CrdtOp.body` without adding an arm here is a type error, not a silent
 * runtime fallthrough. It still returns `unknown` at runtime, because a peer on
 * a newer build can put a body on the wire that this build genuinely has no arm
 * for.
 */
export function opTarget(op: CrdtOp): OpTarget {
  const body = op.body
  switch (body.case) {
    case 'setNodeRegister':
      return { case: 'entity', kind: 'node', id: body.value.nodeId }
    case 'tombstoneNode':
      return { case: 'entity', kind: 'node', id: body.value.nodeId }
    case 'setTabRegister':
      return { case: 'entity', kind: 'tab', id: body.value.tabId }
    case 'tombstoneTab':
      return { case: 'entity', kind: 'tab', id: body.value.tabId }
    case 'reviveTab':
      // ReviveTabOp clears a tab tombstone so a closed subagent tab can re-open.
      // Targets the same tab entity as its tombstone/set siblings.
      return { case: 'entity', kind: 'tab', id: body.value.tab?.tabId ?? '' }
    case 'setFloatingWindowRegister':
      return { case: 'entity', kind: 'fw', id: body.value.windowId }
    case 'tombstoneFloatingWindow':
      return { case: 'entity', kind: 'fw', id: body.value.windowId }
    case 'setWorkspaceRootNode':
      return { case: 'entity', kind: 'ws', id: body.value.workspaceId }
    case 'setWorkspaceRegister':
      return { case: 'entity', kind: 'ws', id: body.value.workspaceId }
    case 'tombstoneWorkspace':
      return { case: 'entity', kind: 'ws', id: body.value.workspaceId }
    case undefined:
      return { case: 'none' }
    default: {
      const exhaustive: never = body
      void exhaustive
      return { case: 'unknown' }
    }
  }
}

/** `opTarget`'s entity spelled as the shared `kind:id` key. */
export function opTargetKey(target: OpTarget): string | null {
  return target.case === 'entity' ? `${target.kind}:${target.id}` : null
}

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

function buildOp(ctx: OpBuilderCtx, body: CrdtOp['body']): CrdtOp {
  return create(CrdtOpSchema, {
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

function setNodeRegister(ctx: OpBuilderCtx, nodeId: string, field: SetNodeRegisterOp['field']): CrdtOp {
  return buildOp(ctx, {
    case: 'setNodeRegister',
    value: create(SetNodeRegisterOpSchema, { nodeId, field }),
  })
}

function setTabRegister(ctx: OpBuilderCtx, tabType: TabType, tabId: string, field: SetTabRegisterOp['field']): CrdtOp {
  return buildOp(ctx, {
    case: 'setTabRegister',
    value: create(SetTabRegisterOpSchema, { tabType, tabId, field }),
  })
}

function setFloatingRegister(ctx: OpBuilderCtx, windowId: string, field: SetFloatingWindowRegisterOp['field']): CrdtOp {
  return buildOp(ctx, {
    case: 'setFloatingWindowRegister',
    value: create(SetFloatingWindowRegisterOpSchema, { windowId, field }),
  })
}

export function setNodeKind(ctx: OpBuilderCtx, nodeId: string, kind: number): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'kind', value: kind })
}

export function setNodeParentId(ctx: OpBuilderCtx, nodeId: string, parentId: string): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'parentId', value: parentId })
}

export function setNodePosition(ctx: OpBuilderCtx, nodeId: string, position: string): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'position', value: position })
}

export function setNodeDirection(ctx: OpBuilderCtx, nodeId: string, direction: number): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'direction', value: direction })
}

export function setNodeRatios(ctx: OpBuilderCtx, nodeId: string, ratios: number[]): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'ratios', value: create(DoubleListSchema, { values: ratios }) })
}

export function setNodeRows(ctx: OpBuilderCtx, nodeId: string, rows: number): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'rows', value: rows })
}

export function setNodeCols(ctx: OpBuilderCtx, nodeId: string, cols: number): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'cols', value: cols })
}

export function setNodeRowRatios(ctx: OpBuilderCtx, nodeId: string, values: number[]): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'rowRatios', value: create(DoubleListSchema, { values }) })
}

export function setNodeColRatios(ctx: OpBuilderCtx, nodeId: string, values: number[]): CrdtOp {
  return setNodeRegister(ctx, nodeId, { case: 'colRatios', value: create(DoubleListSchema, { values }) })
}

export function tombstoneNode(ctx: OpBuilderCtx, nodeId: string): CrdtOp {
  return buildOp(ctx, {
    case: 'tombstoneNode',
    value: create(TombstoneNodeOpSchema, { nodeId }),
  })
}

export function setTabTileId(ctx: OpBuilderCtx, tabType: TabType, tabId: string, tileId: string): CrdtOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'tileId', value: tileId })
}

export function setTabPosition(ctx: OpBuilderCtx, tabType: TabType, tabId: string, position: string): CrdtOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'position', value: position })
}

export function setTabWorkerId(ctx: OpBuilderCtx, tabType: TabType, tabId: string, workerId: string): CrdtOp {
  return setTabRegister(ctx, tabType, tabId, { case: 'workerId', value: workerId })
}

export function tombstoneTab(ctx: OpBuilderCtx, tabType: TabType, tabId: string): CrdtOp {
  return buildOp(ctx, {
    case: 'tombstoneTab',
    value: create(TombstoneTabOpSchema, { tabType, tabId }),
  })
}

/**
 * Build a ReviveTabOp, which clears a tab tombstone so a closed subagent tab
 * can re-open. The revive is an LWW register write on the tombstone register:
 * it clears tombstoneAt only when its HLC is strictly newer. A revived tab
 * must be re-completed by the SAME batch (revive + setTabTileId +
 * setTabPosition + setTabWorkerId together) -- see emitReviveTab.
 */
export function reviveTab(ctx: OpBuilderCtx, tabType: TabType, tabId: string): CrdtOp {
  return buildOp(ctx, {
    case: 'reviveTab',
    value: create(ReviveTabOpSchema, { tab: { tabType, tabId } }),
  })
}

export function setFloatingWorkspaceId(ctx: OpBuilderCtx, windowId: string, workspaceId: string): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'workspaceId', value: workspaceId })
}

export function setFloatingX(ctx: OpBuilderCtx, windowId: string, x: number): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'x', value: x })
}

export function setFloatingY(ctx: OpBuilderCtx, windowId: string, y: number): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'y', value: y })
}

export function setFloatingWidth(ctx: OpBuilderCtx, windowId: string, width: number): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'width', value: width })
}

export function setFloatingHeight(ctx: OpBuilderCtx, windowId: string, height: number): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'height', value: height })
}

export function setFloatingOpacity(ctx: OpBuilderCtx, windowId: string, opacity: number): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'opacity', value: opacity })
}

export function setFloatingRootNodeId(ctx: OpBuilderCtx, windowId: string, rootNodeId: string): CrdtOp {
  return setFloatingRegister(ctx, windowId, { case: 'rootNodeId', value: rootNodeId })
}

export function tombstoneFloatingWindow(ctx: OpBuilderCtx, windowId: string): CrdtOp {
  return buildOp(ctx, {
    case: 'tombstoneFloatingWindow',
    value: create(TombstoneFloatingWindowOpSchema, { windowId }),
  })
}

/**
 * Bundle a list of ops into a fresh OpBatch.
 *
 * `batchId` is a test seam; it defaults to a fresh `generateId()` and every
 * production call site omits it. `batch_id` is the hub's dedup key -- it
 * matches a SubmitOps echo back to its pending batch here, and keys the
 * own-echo suppression in the op-log recorder -- so a deterministic id is what
 * lets a test name the batch it is asserting about. Reusing one is not a silent
 * merge: the hub rejects a replayed batch_id whose body differs with
 * BATCH_REJECTION_OP_ID_COLLISION rather than dropping the ops.
 */
export function newBatch(ops: CrdtOp[], batchId?: string): OpBatch {
  return create(OpBatchSchema, { batchId: batchId ?? generateId(), ops })
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
 * in the tab view so the visible order is identical before and after.
 */
export function liveTabsOnTile(state: UserCrdtState, tileId: string): Array<{ tabType: TabType, tabId: string }> {
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
