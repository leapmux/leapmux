import type { FloatingWindowRecord, HLC, LWWDirection, LWWDouble, LWWDoubles, LWWInt32, LWWNodeKind, LWWString, LWWUint32, NodeRecord, OrgCrdtState, TabRecord } from '~/generated/leapmux/v1/org_crdt_pb'
import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
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
  OrgCrdtStateSchema,
  TabRecordSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import { hlcClone, hlcCmp, hlcIsZero } from './hlc'

/**
 * NewState returns an empty OrgCrdtState seeded with the given org id.
 * Workspaces map is initialized empty; lifecycle paths add entries
 * via manager-internal mutation, not via the op log.
 */
export function newState(orgId: string): OrgCrdtState {
  return create(OrgCrdtStateSchema, {
    orgId,
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
export function applyOp(state: OrgCrdtState, op: OrgOp, canonOverride?: HLC): void {
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
    case 'setFloatingWindowRegister':
      applySetFloatingWindowRegister(state, body.value, canon)
      break
    case 'tombstoneFloatingWindow':
      applyTombstoneFloatingWindow(state, body.value.windowId, canon)
      break
    case 'setWorkspaceRootNode':
      applySetWorkspaceRootNode(state, body.value.workspaceId, body.value.rootNodeId)
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

function ensureNode(state: OrgCrdtState, id: string): NodeRecord {
  let rec = state.nodes[id]
  if (!rec) {
    rec = create(NodeRecordSchema, { nodeId: id })
    state.nodes[id] = rec
  }
  return rec
}

function ensureTab(state: OrgCrdtState, tabId: string, tabType: number): TabRecord {
  let rec = state.tabs[tabId]
  if (!rec) {
    rec = create(TabRecordSchema, { tabType, tabId })
    state.tabs[tabId] = rec
  }
  return rec
}

function ensureFloatingWindow(state: OrgCrdtState, id: string): FloatingWindowRecord {
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

function applySetNodeRegister(state: OrgCrdtState, op: { nodeId: string, field: { case?: string, value?: unknown } }, hlc: HLC): void {
  const rec = ensureNode(state, op.nodeId)
  if (!hlcIsZero(rec.tombstoneAt))
    return
  const f = op.field as { case: string, value: unknown }
  nodeRegisterHandlers[f.case]?.(rec, hlc, f.value)
}

function applyTombstoneNode(state: OrgCrdtState, nodeId: string, hlc: HLC): void {
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

function applySetTabRegister(state: OrgCrdtState, op: { tabType: number, tabId: string, field: { case?: string, value?: unknown } }, hlc: HLC): void {
  const rec = ensureTab(state, op.tabId, op.tabType)
  if (rec.tabType !== op.tabType)
    return
  if (!hlcIsZero(rec.tombstoneAt))
    return
  const f = op.field as { case: string, value: unknown }
  tabRegisterHandlers[f.case]?.(rec, hlc, f.value)
}

function applyTombstoneTab(state: OrgCrdtState, tabType: number, tabId: string, hlc: HLC): void {
  applyTombstoneRecord(
    state.tabs,
    tabId,
    hlc,
    existing => create(TabRecordSchema, {
      // Preserve the existing record's tabType when replacing; only
      // fall back to the op-provided tabType for the fresh-create path.
      tabType: existing?.tabType ?? tabType,
      tabId,
      tombstoneAt: hlcClone(hlc),
    }),
  )
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

function applySetFloatingWindowRegister(state: OrgCrdtState, op: { windowId: string, field: { case?: string, value?: unknown } }, hlc: HLC): void {
  const rec = ensureFloatingWindow(state, op.windowId)
  if (!hlcIsZero(rec.tombstoneAt))
    return
  const f = op.field as { case: string, value: unknown }
  floatingWindowRegisterHandlers[f.case]?.(rec, hlc, f.value)
}

function applyTombstoneFloatingWindow(state: OrgCrdtState, windowId: string, hlc: HLC): void {
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

function applySetWorkspaceRootNode(state: OrgCrdtState, workspaceId: string, rootNodeId: string): void {
  // Lazy-create the `WorkspaceContentsRecord` if this client hasn't
  // seen it yet. The hub seeds an empty record via `MutateInternal`
  // before broadcasting the seed batch, but that mutation is NOT
  // itself broadcast — for any subscriber whose initial
  // `OrgMaterialized` predated the workspace, the
  // `SetWorkspaceRootNode` op is the FIRST signal that the workspace
  // exists. Without lazy-create, this op early-returned and
  // `seedTabIntoNewWorkspace` / `awaitWorkspaceBootstrap` waited
  // forever on `state.workspaces[wsID].rootNodeId`, leaving the
  // newly-created workspace tile-less in the UI.
  let rec = state.workspaces[workspaceId]
  if (!rec) {
    rec = create(WorkspaceContentsRecordSchema, { workspaceId })
    state.workspaces[workspaceId] = rec
  }
  if (rec.rootNodeId === '')
    rec.rootNodeId = rootNodeId
}
