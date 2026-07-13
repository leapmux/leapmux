import type { HLCClock } from './hlc'
import type { FloatingWindowRecord, NodeRecord, OrgCrdtState, TabRecord, WorkspaceContentsRecord } from '~/generated/leapmux/v1/org_crdt_pb'
import type {
  BatchCommitted,
  BatchRejection,
  CommittedOp,
  EntityMaterialized,
  EntityRemoved,
  OpBatch,
  OrgOp,
  WorkspaceProjectionChanged,
} from '~/generated/leapmux/v1/org_ops_pb'
import { clone, create } from '@bufbuild/protobuf'
import {
  FloatingWindowRecordSchema,
  NodeRecordSchema,
  OrgCrdtStateSchema,
  TabRecordSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import { BatchRejectionReason } from '~/generated/leapmux/v1/org_ops_pb'
import { applyOp, newState } from './apply'
import { hlcClone, hlcCmp } from './hlc'

/**
 * PendingOpsState captures the local layered view: confirmed (from
 * the hub) plus speculative (confirmed + still-pending optimistic
 * ops). Mutators submit batches; consumers read the projection of
 * speculativeState.
 */
export interface PendingOpsState {
  confirmedState: OrgCrdtState
  speculativeState: OrgCrdtState
  pendingBatches: OpBatch[]
  currentEpoch: bigint
}

/**
 * Reasons a rejected batch MAY auto-retry without user intervention.
 * Everything else -- including any reason added to the proto later -- is
 * permanent by default, so a newly-introduced server-side rejection can never
 * be silently treated as loop-eligible (an allowlist fails safe where the old
 * denylist failed open: it had already drifted, misclassifying the permanent
 * INVALID_WORKER_REF as retryable). Only EPOCH_REQUIRED is requeued
 * automatically -- after a reconnect refreshes currentEpoch; STALE_EPOCH and
 * every validation/permission denial require the user to re-issue the action.
 */
const RETRYABLE_REJECTIONS = new Set<BatchRejectionReason>([
  BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
])

/**
 * Reasons whose rejection means the client's epoch is stale or missing, so a
 * reconnect must refresh `currentEpoch` before any retry can land. Kept beside
 * RETRYABLE_REJECTIONS so the two orthogonal rejection classifications --
 * "may auto-retry" and "needs an epoch refresh first" -- both live in the module
 * that owns rejection classification, instead of the second one drifting as a
 * hardcoded switch in the submitter layer. STALE_EPOCH needs the refresh but is
 * NOT retryable (the user re-issues); EPOCH_REQUIRED needs both.
 */
const EPOCH_REFRESH_REJECTIONS = new Set<BatchRejectionReason>([
  BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
  BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH,
])

/** PendingOpsManager is the local CRDT-aware queue. */
export class PendingOpsManager {
  state: PendingOpsState
  /**
   * Optional callback fired after any state-mutating method completes.
   * Bridges Solid reactivity by letting the caller bump a signal so
   * memoized projections re-derive when speculativeState changes. The
   * callback is invoked synchronously on the same tick as the
   * mutation; callers are responsible for batching if needed.
   */
  private readonly notify?: () => void

  constructor(
    public readonly orgId: string,
    public readonly clock: HLCClock,
    notify?: () => void,
  ) {
    // Distinct refs: applying speculative ops must not pollute
    // confirmedState even before bootstrap() arrives.
    this.state = {
      confirmedState: newState(orgId),
      speculativeState: newState(orgId),
      pendingBatches: [],
      currentEpoch: 1n,
    }
    this.notify = notify
  }

  /** Seed the confirmed + speculative state from a fresh OrgMaterialized. */
  bootstrap(materialized: { orgId: string, nodes: Record<string, unknown>, tabs: Record<string, unknown>, floatingWindows: Record<string, unknown>, workspaces: Record<string, WorkspaceContentsRecord>, maxHlc?: { physical: bigint, logical: bigint, clientId: string }, currentEpoch: bigint }): void {
    const confirmed = create(OrgCrdtStateSchema, {
      orgId: materialized.orgId,
      nodes: materialized.nodes as never,
      tabs: materialized.tabs as never,
      floatingWindows: materialized.floatingWindows as never,
      workspaces: materialized.workspaces as never,
      maxHlc: hlcClone(materialized.maxHlc as never),
      currentEpoch: materialized.currentEpoch,
    })
    this.state.confirmedState = confirmed
    this.state.currentEpoch = materialized.currentEpoch
    this.recomputeSpeculative()
    this.clock.observe(materialized.maxHlc as never)
    this.notify?.()
  }

  /** Push a fresh local batch and apply it speculatively. */
  submit(batch: OpBatch): void {
    // recomputeSpeculative aliases speculativeState to confirmedState
    // when no batches are pending. Detach before mutating so the
    // applySpeculative below doesn't pollute confirmedState. We clone
    // only the records the new batch will touch — same shape as
    // recomputeSpeculative below.
    if (this.state.speculativeState === this.state.confirmedState)
      this.state.speculativeState = cloneStateForBatches(this.state.confirmedState, [batch])
    this.state.pendingBatches.push(batch)
    for (const op of batch.ops)
      applySpeculative(this.state.speculativeState, op)
    this.notify?.()
  }

  /**
   * Apply a remote batch (or our own echo). When the incoming batch's
   * id matches a locally-pending batch, this is an echo of our own
   * submission: drop the pending batch and apply the canonical ops to
   * confirmedState. Otherwise, apply each op fresh.
   *
   * Note: with per-subscriber visibility filtering, the wire batch may
   * contain only a subset of the original ops (workspace-redacted ones
   * are stripped). For our own echo this can't happen (the originator
   * is always pre/post-visible), so a partial echo would indicate a
   * bug; we still apply whatever arrives and drop the pending batch.
   */
  consumeRemote(batch: OpBatch): void {
    const idx = this.state.pendingBatches.findIndex(b => b.batchId === batch.batchId)
    for (const op of batch.ops) {
      this.clock.observe(op.canonicalHlc)
      applyOp(this.state.confirmedState, op)
    }
    if (idx >= 0)
      this.state.pendingBatches.splice(idx, 1)
    this.recomputeSpeculative()
    this.notify?.()
  }

  /** Apply a BatchCommitted: replace pending batch's HLCs with canonical and apply to confirmed. */
  consumeBatchCommitted(batchId: string, committed: BatchCommitted): void {
    const idx = this.state.pendingBatches.findIndex(b => b.batchId === batchId)
    if (idx < 0)
      return
    const batch = this.state.pendingBatches[idx]
    const byOpId = new Map<string, CommittedOp>()
    for (const c of committed.committed) byOpId.set(c.opId, c)
    for (const op of batch.ops) {
      const c = byOpId.get(op.opId)
      if (!c)
        continue
      op.canonicalHlc = c.canonicalHlc
      this.clock.observe(c.canonicalHlc)
      applyOp(this.state.confirmedState, op)
    }
    this.state.pendingBatches.splice(idx, 1)
    this.state.currentEpoch = committed.epoch
    this.recomputeSpeculative()
    this.notify?.()
  }

  /**
   * Apply a BatchRejection.
   *
   * A RETRYABLE rejection (EPOCH_REQUIRED) is NOT terminal -- the submitter
   * requeues the batch -- so its optimistic ops are KEPT in the pending list and
   * stay applied to speculativeState: the edit remains visible across the
   * reconnect+retry window (no revert-then-reapply flicker, no transient
   * worker/CRDT divergence), and the retry's BatchCommitted reconciles it just
   * like any in-flight batch. A non-retryable rejection (a permanent denial,
   * STALE_EPOCH, or a transport give-up passing reason 0) IS terminal, so the
   * batch is dropped and its optimistic ops reverted. If the submitter later
   * gives up on a kept retryable batch (retry cap, or no reconnect handler to
   * refresh the epoch), it calls revertPendingBatch to drop it then.
   */
  consumeBatchRejected(batchId: string, rejection: BatchRejection): { reason: number, offendingOpId: string, retryable: boolean, needsEpochRefresh: boolean } {
    const retryable = RETRYABLE_REJECTIONS.has(rejection.reason)
    if (!retryable)
      this.revertPendingBatch(batchId)
    return {
      reason: rejection.reason,
      offendingOpId: rejection.offendingOpId,
      retryable,
      needsEpochRefresh: EPOCH_REFRESH_REJECTIONS.has(rejection.reason),
    }
  }

  /**
   * Drop a pending batch and revert its optimistic ops from speculativeState.
   * Used for a terminal rejection and when the submitter finally gives up on a
   * retryable batch whose optimistic ops it had kept applied.
   */
  revertPendingBatch(batchId: string): void {
    const idx = this.state.pendingBatches.findIndex(b => b.batchId === batchId)
    if (idx < 0)
      return
    this.state.pendingBatches.splice(idx, 1)
    this.recomputeSpeculative()
    this.notify?.()
  }

  /**
   * Apply an EntityMaterialized: install the full record into
   * confirmedState's matching map slot. The hub sends this when an
   * entity ENTERS the subscriber's allowed set as a side effect of a
   * workspace move; raw move ops are suppressed for becoming-visible
   * subscribers (they would carry pre-state from a hidden workspace),
   * so this event is the only way a fresh entity arrives on this
   * client.
   */
  consumeEntityMaterialized(evt: EntityMaterialized): void {
    if (evt.atHlc)
      this.clock.observe(evt.atHlc)
    const entity = evt.entity
    switch (entity.case) {
      case 'tab': {
        const t = entity.value as TabRecord
        this.state.confirmedState.tabs[t.tabId] = t
        break
      }
      case 'floatingWindow': {
        const fw = entity.value as FloatingWindowRecord
        this.state.confirmedState.floatingWindows[fw.windowId] = fw
        break
      }
      case 'node': {
        const n = entity.value as NodeRecord
        this.state.confirmedState.nodes[n.nodeId] = n
        break
      }
    }
    this.recomputeSpeculative()
    this.notify?.()
  }

  /**
   * Apply an EntityRemoved: delete the entity from confirmedState
   * AND drop any pending ops touching that entity (otherwise a
   * pending mutation would resurrect a redacted entity from partial
   * state). EntityRemoved is NOT a CRDT tombstone — it's a view-state
   * notification triggered by a workspace move that pushed the entity
   * out of the subscriber's allowed set.
   *
   * Returns whether any pending ops were dropped so the caller can
   * surface a warn-toast when the dropped op was active-tab-related.
   */
  consumeEntityRemoved(evt: EntityRemoved): { droppedPending: boolean } {
    if (evt.atHlc)
      this.clock.observe(evt.atHlc)
    let droppedPending = false
    const entity = evt.entity
    switch (entity.case) {
      case 'tab': {
        const ident = entity.value
        delete this.state.confirmedState.tabs[ident.tabId]
        droppedPending = this.dropPendingByPredicate((op) => {
          const body = op.body
          if (body.case === 'setTabRegister' || body.case === 'tombstoneTab')
            return body.value.tabId === ident.tabId
          return false
        })
        break
      }
      case 'windowId': {
        const id = entity.value
        delete this.state.confirmedState.floatingWindows[id]
        droppedPending = this.dropPendingByPredicate((op) => {
          const body = op.body
          if (body.case === 'setFloatingWindowRegister' || body.case === 'tombstoneFloatingWindow')
            return body.value.windowId === id
          return false
        })
        break
      }
      case 'nodeId': {
        const id = entity.value
        delete this.state.confirmedState.nodes[id]
        droppedPending = this.dropPendingByPredicate((op) => {
          const body = op.body
          if (body.case === 'setNodeRegister' || body.case === 'tombstoneNode')
            return body.value.nodeId === id
          return false
        })
        break
      }
    }
    this.recomputeSpeculative()
    this.notify?.()
    return { droppedPending }
  }

  /** Atomically replace or remove one workspace's delivered projection. */
  consumeWorkspaceProjection(evt: WorkspaceProjectionChanged): { droppedPending: boolean } {
    const workspaceId = evt.workspaceId
    if (!workspaceId)
      return { droppedPending: false }
    if (evt.change.case !== 'granted' && evt.change.case !== 'revoked')
      return { droppedPending: false }
    if (evt.change.case === 'granted' && !evt.change.value.workspaces[workspaceId])
      return { droppedPending: false }

    const confirmedEntities = workspaceEntityIDs(this.state.confirmedState, workspaceId)
    const speculativeEntities = this.state.speculativeState === this.state.confirmedState
      ? confirmedEntities
      : workspaceEntityIDs(this.state.speculativeState, workspaceId)
    const affected = mergeWorkspaceEntityIDs(confirmedEntities, speculativeEntities)
    const projection = evt.change.case === 'granted' ? evt.change.value : undefined
    replaceWorkspaceProjection(this.state.confirmedState, workspaceId, confirmedEntities, projection)

    if (projection) {
      if (projection.maxHlc && hlcCmp(projection.maxHlc, this.state.confirmedState.maxHlc) > 0)
        this.state.confirmedState.maxHlc = hlcClone(projection.maxHlc)
      if (projection.currentEpoch > this.state.currentEpoch) {
        this.state.currentEpoch = projection.currentEpoch
        this.state.confirmedState.currentEpoch = projection.currentEpoch
      }
      this.clock.observe(projection.maxHlc)
    }

    const droppedPending = this.dropPendingByPredicate((op) => {
      const body = op.body
      switch (body.case) {
        case 'setNodeRegister':
        case 'tombstoneNode':
          return affected.nodes.has(body.value.nodeId)
        case 'setTabRegister':
        case 'tombstoneTab':
          return affected.tabs.has(body.value.tabId)
        case 'setFloatingWindowRegister':
        case 'tombstoneFloatingWindow':
          return affected.floatingWindows.has(body.value.windowId)
        case 'setWorkspaceRootNode':
          return body.value.workspaceId === workspaceId
        default:
          return false
      }
    })
    this.recomputeSpeculative()
    this.notify?.()
    return { droppedPending }
  }

  /** dropPendingByPredicate removes every op for which `pred` returns true and returns whether any ops were dropped. */
  private dropPendingByPredicate(pred: (op: OrgOp) => boolean): boolean {
    let dropped = false
    for (const batch of this.state.pendingBatches) {
      const before = batch.ops.length
      batch.ops = batch.ops.filter(op => !pred(op))
      if (batch.ops.length !== before)
        dropped = true
    }
    this.state.pendingBatches = this.state.pendingBatches.filter(b => b.ops.length > 0)
    return dropped
  }

  /**
   * Re-fold every pending batch on top of confirmedState. Public so
   * the caller (useOrgEvents) can flush after directly mutating
   * confirmedState in response to EntityMaterialized / EntityRemoved
   * events.
   *
   * Fast path: when no batches are pending, speculativeState aliases
   * confirmedState — we skip the clone-and-replay since they're
   * guaranteed equal. `submit` detaches the alias before its first
   * mutation so the alias never escapes as a writable reference.
   */
  recomputeSpeculative(): void {
    if (this.state.pendingBatches.length === 0) {
      this.state.speculativeState = this.state.confirmedState
      return
    }
    const cloned = cloneStateForBatches(this.state.confirmedState, this.state.pendingBatches)
    for (const batch of this.state.pendingBatches) {
      for (const op of batch.ops)
        applySpeculative(cloned, op)
    }
    this.state.speculativeState = cloned
  }
}

interface WorkspaceStateProjection {
  nodes: Record<string, NodeRecord>
  tabs: Record<string, TabRecord>
  floatingWindows: Record<string, FloatingWindowRecord>
  workspaces: Record<string, WorkspaceContentsRecord>
}

interface WorkspaceEntityIDs {
  nodes: Set<string>
  tabs: Set<string>
  floatingWindows: Set<string>
}

function workspaceEntityIDs(state: WorkspaceStateProjection, workspaceId: string): WorkspaceEntityIDs {
  const roots = new Set<string>()
  const workspaceRoot = state.workspaces[workspaceId]?.rootNodeId
  if (workspaceRoot)
    roots.add(workspaceRoot)

  const floatingWindows = new Set<string>()
  for (const [id, floatingWindow] of Object.entries(state.floatingWindows)) {
    if (floatingWindow.workspaceId?.value !== workspaceId)
      continue
    floatingWindows.add(id)
    const rootNodeId = floatingWindow.rootNodeId
    if (rootNodeId)
      roots.add(rootNodeId)
  }

  const children = new Map<string, string[]>()
  for (const [id, node] of Object.entries(state.nodes)) {
    const parentId = node.parentId
    if (!parentId)
      continue
    const siblings = children.get(parentId)
    if (siblings)
      siblings.push(id)
    else
      children.set(parentId, [id])
  }
  const nodes = new Set<string>()
  const queue = [...roots]
  for (let cursor = 0; cursor < queue.length; cursor++) {
    const id = queue[cursor]!
    if (nodes.has(id) || !state.nodes[id])
      continue
    nodes.add(id)
    // Enqueue children one at a time rather than `queue.push(...children)`: a
    // spread of a very large child list would exceed the engine's max argument
    // count and throw, and this is allocation-free besides.
    for (const child of children.get(id) ?? [])
      queue.push(child)
  }
  const tabs = new Set<string>()
  for (const [id, tab] of Object.entries(state.tabs)) {
    const tileId = tab.tileId?.value
    if (tileId && nodes.has(tileId))
      tabs.add(id)
  }
  return { nodes, tabs, floatingWindows }
}

function mergeWorkspaceEntityIDs(a: WorkspaceEntityIDs, b: WorkspaceEntityIDs): WorkspaceEntityIDs {
  return {
    nodes: new Set([...a.nodes, ...b.nodes]),
    tabs: new Set([...a.tabs, ...b.tabs]),
    floatingWindows: new Set([...a.floatingWindows, ...b.floatingWindows]),
  }
}

function replaceWorkspaceProjection(
  state: OrgCrdtState,
  workspaceId: string,
  previous: WorkspaceEntityIDs,
  projection?: WorkspaceStateProjection,
): void {
  delete state.workspaces[workspaceId]
  for (const id of previous.nodes)
    delete state.nodes[id]
  for (const id of previous.tabs)
    delete state.tabs[id]
  for (const id of previous.floatingWindows)
    delete state.floatingWindows[id]
  if (!projection)
    return

  const workspace = projection.workspaces[workspaceId]
  if (!workspace)
    return
  state.workspaces[workspaceId] = clone(WorkspaceContentsRecordSchema, workspace)
  const entities = workspaceEntityIDs(projection, workspaceId)
  copySelectedRecords(state.nodes, projection.nodes, entities.nodes, record => clone(NodeRecordSchema, record))
  copySelectedRecords(state.tabs, projection.tabs, entities.tabs, record => clone(TabRecordSchema, record))
  copySelectedRecords(
    state.floatingWindows,
    projection.floatingWindows,
    entities.floatingWindows,
    record => clone(FloatingWindowRecordSchema, record),
  )
}

function copySelectedRecords<T>(
  target: Record<string, T>,
  source: Record<string, T>,
  ids: ReadonlySet<string>,
  copy: (record: T) => T,
): void {
  for (const id of ids) {
    const record = source[id]
    if (record)
      target[id] = copy(record)
  }
}

/**
 * applySpeculative wraps applyOp with the speculative HLC selection
 * shared by both submit() and recomputeSpeculative(): prefer the
 * canonical HLC (assigned by the hub on commit) when present,
 * otherwise fall back to the local client_hlc as a per-apply override.
 * applySpeculative never mutates the op — it passes the client_hlc as a
 * per-apply override rather than writing op.canonicalHlc — so wire-emit
 * reads the same batch object later and the hub still rejects ops that
 * arrive with canonical_hlc pre-set. (canonicalHlc IS written, but only
 * by consumeBatchCommitted on commit, after which the op is never re-sent.)
 */
function applySpeculative(state: OrgCrdtState, op: OrgOp): void {
  applyOp(state, op, op.canonicalHlc ? undefined : (op.clientHlc ?? undefined))
}

/**
 * cloneStateForBatches returns a state where every record the
 * `batches` will mutate is deep-cloned, and every other record is
 * shared by reference with `state`. Top-level maps are always shallow-
 * copied so that creating new records via apply (e.g. lazy-ensure or
 * tombstone-replace) lands in the cloned map without leaking into
 * `state`.
 *
 * apply.ts mutates a record in place for `set*Register` ops, but
 * tombstone ops REPLACE the map slot with a fresh record — those
 * don't need pre-cloning. Similarly setWorkspaceRootNode mutates the
 * workspace record in place, so we pre-clone its slot when present.
 *
 * Mirrors the backend's `CloneStateForBatch` (state.go).
 */
function cloneStateForBatches(state: OrgCrdtState, batches: OpBatch[]): OrgCrdtState {
  const nodes: Record<string, NodeRecord> = { ...state.nodes }
  const tabs: Record<string, TabRecord> = { ...state.tabs }
  const floatingWindows: Record<string, FloatingWindowRecord> = { ...state.floatingWindows }
  const workspaces = { ...state.workspaces }

  const clonedNodes = new Set<string>()
  const clonedTabs = new Set<string>()
  const clonedFws = new Set<string>()
  const clonedWss = new Set<string>()
  for (const batch of batches) {
    for (const op of batch.ops) {
      const body = op.body
      switch (body.case) {
        case 'setNodeRegister': {
          const id = body.value.nodeId
          if (!clonedNodes.has(id) && nodes[id]) {
            nodes[id] = clone(NodeRecordSchema, nodes[id])
            clonedNodes.add(id)
          }
          break
        }
        case 'setTabRegister': {
          const id = body.value.tabId
          if (!clonedTabs.has(id) && tabs[id]) {
            tabs[id] = clone(TabRecordSchema, tabs[id])
            clonedTabs.add(id)
          }
          break
        }
        case 'setFloatingWindowRegister': {
          const id = body.value.windowId
          if (!clonedFws.has(id) && floatingWindows[id]) {
            floatingWindows[id] = clone(FloatingWindowRecordSchema, floatingWindows[id])
            clonedFws.add(id)
          }
          break
        }
        case 'setWorkspaceRootNode': {
          const id = body.value.workspaceId
          // SetWorkspaceRootNode is set-once via applyOp; if rootNodeId
          // is already non-empty the op is a no-op and cloning the
          // record would be wasted work. Only clone when the slot is
          // empty or the workspace record is yet to be materialized.
          if (!clonedWss.has(id) && workspaces[id] && workspaces[id].rootNodeId === '') {
            workspaces[id] = clone(WorkspaceContentsRecordSchema, workspaces[id])
            clonedWss.add(id)
          }
          break
        }
        // Tombstone ops replace the map slot with a fresh record;
        // they do not mutate the pre-existing record, so no pre-clone
        // is needed.
      }
    }
  }

  return create(OrgCrdtStateSchema, {
    orgId: state.orgId,
    nodes,
    tabs,
    floatingWindows,
    workspaces,
    maxHlc: hlcClone(state.maxHlc),
    compactionWatermark: hlcClone(state.compactionWatermark),
    currentEpoch: state.currentEpoch,
    epochStartedAt: state.epochStartedAt,
  })
}
