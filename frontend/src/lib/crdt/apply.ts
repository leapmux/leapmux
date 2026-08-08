import type { FloatingWindowRecord, HLC, LWWDirection, LWWDouble, LWWDoubles, LWWInt32, LWWNodeKind, LWWString, LWWUint32, NodeRecord, TabRecord, UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { CrdtOp } from '~/generated/leapmux/v1/user_ops_pb'
import { create } from '@bufbuild/protobuf'
import {
  DoubleListSchema,
  FloatingWindowRecordSchema,
  LWWDirectionSchema,
  LWWDoubleSchema,
  LWWDoublesSchema,
  LWWInt32Schema,
  LWWNodeKindSchema,
  LWWStringSchema,
  LWWUint32Schema,
  NodeRecordSchema,
  TabRecordSchema,
  UserCrdtStateSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/user_crdt_pb'
import { hlcClone, hlcCmp, hlcIsZero } from './hlc'

/**
 * NewState returns an empty UserCrdtState seeded with the given user id.
 * Workspaces map is initialized empty; the lifecycle create/delete paths add
 * and remove entries via SetWorkspaceRegisterOp / TombstoneWorkspaceOp in the
 * op log (the same serialized pipeline every other op flows through).
 */
export function newState(userId: string): UserCrdtState {
  return create(UserCrdtStateSchema, {
    userId,
    nodes: {},
    tabs: {},
    floatingWindows: {},
    workspaces: {},
    currentEpoch: 1n,
  })
}

/**
 * Apply mutates state in place, applying op with its canonical_hlc
 * already set. Mirrors backend `state.go` byte-for-byte; the parity
 * test asserts deterministic state across permutations of a
 * validated committed op log.
 *
 * `canonOverride` lets the speculative path stamp a per-apply HLC
 * (the local client_hlc) without mutating the persisted op — the hub
 * later assigns the real canonical HLC and the op is re-applied with
 * that value via `consumeBatchCommitted` / `recomputeSpeculative`.
 */
export function applyOp(state: UserCrdtState, op: CrdtOp, canonOverride?: HLC): void {
  const canon = canonOverride ?? op.canonicalHlc
  if (!canon)
    return
  if (hlcCmp(canon, state.maxHlc) > 0) {
    state.maxHlc = hlcClone(canon)
  }
  const body = op.body
  switch (body.case) {
    case 'setNodeRegister':
      applySetNodeRegister(state, body.value, canon)
      break
    case 'tombstoneNode':
      applyTombstoneNode(state, body.value.nodeId, canon)
      break
    case 'setTabRegister':
      applySetTabRegister(state, body.value, canon)
      break
    case 'tombstoneTab':
      applyTombstoneTab(state, body.value.tabType, body.value.tabId, canon)
      break
    case 'reviveTab':
      applyReviveTab(state, body.value.tab?.tabType ?? 0, body.value.tab?.tabId ?? '', canon)
      break
    case 'setFloatingWindowRegister':
      applySetFloatingWindowRegister(state, body.value, canon)
      break
    case 'tombstoneFloatingWindow':
      applyTombstoneFloatingWindow(state, body.value.windowId, canon)
      break
    case 'setWorkspaceRootNode':
      applySetWorkspaceRootNode(state, body.value.workspaceId, body.value.rootNodeId)
      break
    case 'setWorkspaceRegister':
      applySetWorkspaceRegister(state, body.value.workspaceId)
      break
    case 'tombstoneWorkspace':
      applyTombstoneWorkspace(state, body.value.workspaceId)
      break
  }
}

/** -0.0 → +0.0 normalization. Object.is distinguishes -0/+0. */
function canonicalizeZero(v: number): number {
  return Object.is(v, -0) ? 0 : v
}

function canonicalizeZeros(values: number[]): number[] {
  return values.map(canonicalizeZero)
}

function shouldWrite(currentHLC: HLC | undefined, opHLC: HLC): boolean {
  return hlcCmp(opHLC, currentHLC) > 0
}

function lwwString(value: string, hlc: HLC): LWWString {
  return create(LWWStringSchema, { value, hlc: hlcClone(hlc) })
}

function lwwInt32(value: number, hlc: HLC): LWWInt32 {
  return create(LWWInt32Schema, { value, hlc: hlcClone(hlc) })
}

function lwwUint32(value: number, hlc: HLC): LWWUint32 {
  return create(LWWUint32Schema, { value, hlc: hlcClone(hlc) })
}

function lwwDouble(value: number, hlc: HLC): LWWDouble {
  return create(LWWDoubleSchema, { value: canonicalizeZero(value), hlc: hlcClone(hlc) })
}

function lwwDoubles(values: number[], hlc: HLC): LWWDoubles {
  const list = create(DoubleListSchema, { values: canonicalizeZeros(values) })
  return create(LWWDoublesSchema, { value: list, hlc: hlcClone(hlc) })
}

function lwwDirection(value: number, hlc: HLC): LWWDirection {
  return create(LWWDirectionSchema, { value, hlc: hlcClone(hlc) })
}

function lwwNodeKind(value: number, hlc: HLC): LWWNodeKind {
  return create(LWWNodeKindSchema, { value, hlc: hlcClone(hlc) })
}

function ensureNode(state: UserCrdtState, id: string): NodeRecord {
  let rec = state.nodes[id]
  if (!rec) {
    rec = create(NodeRecordSchema, { nodeId: id })
    state.nodes[id] = rec
  }
  return rec
}

function ensureTab(state: UserCrdtState, tabId: string, tabType: number): TabRecord {
  let rec = state.tabs[tabId]
  if (!rec) {
    rec = create(TabRecordSchema, { tabType, tabId })
    state.tabs[tabId] = rec
  }
  return rec
}

function ensureFloatingWindow(state: UserCrdtState, id: string): FloatingWindowRecord {
  let rec = state.floatingWindows[id]
  if (!rec) {
    rec = create(FloatingWindowRecordSchema, { windowId: id })
    state.floatingWindows[id] = rec
  }
  return rec
}

// Register-field tables for each of the three register families.
// Each handler reads `op.field.value` (already narrowed by the dispatch)
// and either writes via `lww*` if `shouldWrite` clears, or applies the
// register's set-once rule (`parentId` / `rootNodeId`). Unknown field
// cases fall through to a no-op when no handler is registered for them.
type NodeFieldHandler = (rec: NodeRecord, hlc: HLC, value: unknown) => void
const nodeRegisterHandlers: Record<string, NodeFieldHandler> = {
  kind: (rec, hlc, value) => {
    if (shouldWrite(rec.kind?.hlc, hlc))
      rec.kind = lwwNodeKind(value as number, hlc)
  },
  parentId: (rec, _hlc, value) => {
    // Set-once: subsequent ops must be ignored regardless of HLC order.
    if (rec.parentId === '')
      rec.parentId = value as string
  },
  position: (rec, hlc, value) => {
    if (shouldWrite(rec.position?.hlc, hlc))
      rec.position = lwwString(value as string, hlc)
  },
  direction: (rec, hlc, value) => {
    if (shouldWrite(rec.direction?.hlc, hlc))
      rec.direction = lwwDirection(value as number, hlc)
  },
  ratios: (rec, hlc, value) => {
    if (shouldWrite(rec.ratios?.hlc, hlc))
      rec.ratios = lwwDoubles((value as { values: number[] }).values, hlc)
  },
  rows: (rec, hlc, value) => {
    if (shouldWrite(rec.rows?.hlc, hlc))
      rec.rows = lwwUint32(value as number, hlc)
  },
  cols: (rec, hlc, value) => {
    if (shouldWrite(rec.cols?.hlc, hlc))
      rec.cols = lwwUint32(value as number, hlc)
  },
  rowRatios: (rec, hlc, value) => {
    if (shouldWrite(rec.rowRatios?.hlc, hlc))
      rec.rowRatios = lwwDoubles((value as { values: number[] }).values, hlc)
  },
  colRatios: (rec, hlc, value) => {
    if (shouldWrite(rec.colRatios?.hlc, hlc))
      rec.colRatios = lwwDoubles((value as { values: number[] }).values, hlc)
  },
}

function applySetNodeRegister(state: UserCrdtState, op: { nodeId: string, field: { case?: string, value?: unknown } }, hlc: HLC): void {
  const rec = ensureNode(state, op.nodeId)
  if (!hlcIsZero(rec.tombstoneAt))
    return
  const f = op.field as { case: string, value: unknown }
  nodeRegisterHandlers[f.case]?.(rec, hlc, f.value)
}

function applyTombstoneNode(state: UserCrdtState, nodeId: string, hlc: HLC): void {
  applyTombstoneRecord(
    state.nodes,
    nodeId,
    hlc,
    () => create(NodeRecordSchema, { nodeId, tombstoneAt: hlcClone(hlc) }),
  )
}

type TabFieldHandler = (rec: TabRecord, hlc: HLC, value: unknown) => void
const tabRegisterHandlers: Record<string, TabFieldHandler> = {
  tileId: (rec, hlc, value) => {
    if (shouldWrite(rec.tileId?.hlc, hlc))
      rec.tileId = lwwString(value as string, hlc)
  },
  position: (rec, hlc, value) => {
    if (shouldWrite(rec.position?.hlc, hlc))
      rec.position = lwwString(value as string, hlc)
  },
  workerId: (rec, hlc, value) => {
    if (shouldWrite(rec.workerId?.hlc, hlc))
      rec.workerId = lwwString(value as string, hlc)
  },
  displayMode: (rec, hlc, value) => {
    if (shouldWrite(rec.displayMode?.hlc, hlc))
      rec.displayMode = lwwInt32(value as number, hlc)
  },
  fileViewMode: (rec, hlc, value) => {
    if (shouldWrite(rec.fileViewMode?.hlc, hlc))
      rec.fileViewMode = lwwInt32(value as number, hlc)
  },
  fileDiffBase: (rec, hlc, value) => {
    if (shouldWrite(rec.fileDiffBase?.hlc, hlc))
      rec.fileDiffBase = lwwString(value as string, hlc)
  },
}

function applySetTabRegister(state: UserCrdtState, op: { tabType: number, tabId: string, field: { case?: string, value?: unknown } }, hlc: HLC): void {
  const rec = ensureTab(state, op.tabId, op.tabType)
  if (rec.tabType !== op.tabType)
    return
  if (!hlcIsZero(rec.tombstoneAt))
    return
  const f = op.field as { case: string, value: unknown }
  tabRegisterHandlers[f.case]?.(rec, hlc, f.value)
}

function applyTombstoneTab(state: UserCrdtState, tabType: number, tabId: string, hlc: HLC): void {
  const existing = state.tabs[tabId]
  // The tombstone is a true LWW register over both tombstone and revive
  // writes: a tombstone applies only when its HLC is newer than the current
  // tombstone AND the last revive (revivedAt). Mirrors the hub applyTombstoneTab.
  if (!existing || (hlcCmp(hlc, existing.tombstoneAt) > 0 && hlcCmp(hlc, existing.revivedAt) > 0)) {
    state.tabs[tabId] = create(TabRecordSchema, {
      // Preserve the existing record's tabType when replacing; only
      // fall back to the op-provided tabType for the fresh-create path.
      tabType: existing?.tabType ?? tabType,
      tabId,
      tombstoneAt: hlcClone(hlc),
    })
  }
}

/**
 * Mirror of the hub's applyReviveTab: clears a tab's tombstone so a closed
 * subagent tab can re-open. LWW on the tombstone register -- only clears when
 * the revive's HLC is strictly newer than the tombstone. Preserves the record
 * in place (the same batch's Set ops repopulate the registers). A revive of
 * a never-seen tab materializes a live record.
 *
 * revivedAt is a monotone max (mirrors the hub): a redelivered or out-of-order
 * revive can never regress revivedAt below a prior successful revive, so a
 * later tombstone newer than the stale revive but older than the real one
 * cannot re-close a tab the user reopened.
 */
function applyReviveTab(state: UserCrdtState, tabType: number, tabId: string, hlc: HLC): void {
  if (!tabId)
    return
  const existing = state.tabs[tabId]
  if (!existing) {
    state.tabs[tabId] = create(TabRecordSchema, { tabType, tabId, revivedAt: hlcClone(hlc) })
    return
  }
  if (hlcCmp(hlc, existing.tombstoneAt) > 0) {
    existing.tombstoneAt = undefined
  }
  // Monotone max: keep the newer of the incoming and existing revive HLC. A
  // losing revive (the branch above did not fire) must not regress revivedAt.
  if (hlcCmp(hlc, existing.revivedAt) > 0) {
    existing.revivedAt = hlcClone(hlc)
  }
}

type FloatingWindowFieldHandler = (rec: FloatingWindowRecord, hlc: HLC, value: unknown) => void
const floatingWindowRegisterHandlers: Record<string, FloatingWindowFieldHandler> = {
  workspaceId: (rec, hlc, value) => {
    if (shouldWrite(rec.workspaceId?.hlc, hlc))
      rec.workspaceId = lwwString(value as string, hlc)
  },
  x: (rec, hlc, value) => {
    if (shouldWrite(rec.x?.hlc, hlc))
      rec.x = lwwDouble(value as number, hlc)
  },
  y: (rec, hlc, value) => {
    if (shouldWrite(rec.y?.hlc, hlc))
      rec.y = lwwDouble(value as number, hlc)
  },
  width: (rec, hlc, value) => {
    if (shouldWrite(rec.width?.hlc, hlc))
      rec.width = lwwDouble(value as number, hlc)
  },
  height: (rec, hlc, value) => {
    if (shouldWrite(rec.height?.hlc, hlc))
      rec.height = lwwDouble(value as number, hlc)
  },
  opacity: (rec, hlc, value) => {
    if (shouldWrite(rec.opacity?.hlc, hlc))
      rec.opacity = lwwDouble(value as number, hlc)
  },
  rootNodeId: (rec, _hlc, value) => {
    // Set-once: subsequent ops must be ignored regardless of HLC order.
    if (rec.rootNodeId === '')
      rec.rootNodeId = value as string
  },
}

function applySetFloatingWindowRegister(state: UserCrdtState, op: { windowId: string, field: { case?: string, value?: unknown } }, hlc: HLC): void {
  const rec = ensureFloatingWindow(state, op.windowId)
  if (!hlcIsZero(rec.tombstoneAt))
    return
  const f = op.field as { case: string, value: unknown }
  floatingWindowRegisterHandlers[f.case]?.(rec, hlc, f.value)
}

function applyTombstoneFloatingWindow(state: UserCrdtState, windowId: string, hlc: HLC): void {
  applyTombstoneRecord(
    state.floatingWindows,
    windowId,
    hlc,
    () => create(FloatingWindowRecordSchema, { windowId, tombstoneAt: hlcClone(hlc) }),
  )
}

/**
 * Shared tombstone path: if no record exists, install a fresh
 * tombstoned record via `init(undefined)`; otherwise, when the new HLC
 * is later than the current `tombstoneAt`, REPLACE the record with a
 * fresh tombstone (wiping all register fields), via `init(existing)`.
 * Replacement-on-newer is the existing byte-for-byte parity behavior
 * with the Go-side `state.go`; the `init` lambda's `existing` arg lets
 * callers preserve immutable identity fields (e.g. Tab's `tabType`)
 * across the wipe.
 */
function applyTombstoneRecord<R extends { tombstoneAt?: HLC }>(
  map: Record<string, R>,
  id: string,
  hlc: HLC,
  init: (existing: R | undefined) => R,
): void {
  const existing = map[id]
  if (!existing) {
    map[id] = init(undefined)
    return
  }
  if (hlcCmp(hlc, existing.tombstoneAt) > 0)
    map[id] = init(existing)
}

function applySetWorkspaceRootNode(state: UserCrdtState, workspaceId: string, rootNodeId: string): void {
  // Lazy-create the `WorkspaceContentsRecord` if this client hasn't
  // seen it yet. The hub's lifecycle create batch seeds the record via a
  // `SetWorkspaceRegisterOp` in the SAME batch as this op, so a subscriber
  // that admits the workspace receives both and the record is normally
  // already present when this runs. This lazy-create is the bootstrap-replay
  // safety net for op logs written before `SetWorkspaceRegisterOp` existed
  // (a `SetWorkspaceRootNode` op with no companion register), or for a
  // subscriber whose initial `UserMaterialized` predated the workspace and
  // whose filter misses the seed batch. Either leaves the op with no record
  // to land on — and `seedTabIntoNewWorkspace` / `awaitWorkspaceBootstrap`
  // would wait forever on `state.workspaces[wsID].rootNodeId`, leaving the
  // newly-created workspace tile-less in the UI.
  let rec = state.workspaces[workspaceId]
  if (!rec) {
    rec = create(WorkspaceContentsRecordSchema, { workspaceId })
    state.workspaces[workspaceId] = rec
  }
  if (rec.rootNodeId === '')
    rec.rootNodeId = rootNodeId
}

/**
 * applySetWorkspaceRegister seeds the WorkspaceContentsRecord map entry for a
 * workspace (root_node_id empty). Idempotent: a workspace that already exists
 * is left untouched, so a re-drained create seed batch (after a transient
 * consume fault) never clobbers a workspace the user has since rooted or
 * populated. Mirrors backend `applySetWorkspaceRegister`.
 */
function applySetWorkspaceRegister(state: UserCrdtState, workspaceId: string): void {
  if (!workspaceId)
    return
  if (!state.workspaces[workspaceId]) {
    state.workspaces[workspaceId] = create(WorkspaceContentsRecordSchema, { workspaceId })
  }
}

/**
 * applyTombstoneWorkspace removes the WorkspaceContentsRecord map entry.
 * Idempotent: deleting an already-absent workspace is a no-op, so a re-drained
 * delete batch (after a transient consume fault) is safe. Mirrors backend
 * `applyTombstoneWorkspace`.
 */
function applyTombstoneWorkspace(state: UserCrdtState, workspaceId: string): void {
  if (!workspaceId)
    return
  delete state.workspaces[workspaceId]
}
