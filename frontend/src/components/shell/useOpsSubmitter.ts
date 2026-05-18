import type { BatchRejection, OpBatch } from '~/generated/leapmux/v1/org_ops_pb'
import type { PendingOpsManager } from '~/lib/crdt'
import { onCleanup } from 'solid-js'
import { orgCRDTClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { BatchRejectionReason } from '~/generated/leapmux/v1/org_ops_pb'
import { createExponentialBackoff } from '~/lib/retry'

/**
 * SUBMIT_FLUSH_MS is the aggregator window — every queued batch
 * inside this many ms goes out in a single SubmitOps RPC. 16ms keeps
 * us inside a single animation frame so a flurry of UI mutations
 * (drag-resize ratios, multi-tile open) lands in one round-trip.
 */
const SUBMIT_FLUSH_MS = 16

/**
 * MAX_TRANSPORT_RETRIES caps the number of times the submitter will
 * automatically re-send a batch after a transport-level failure. Each
 * retry uses the original `op_id`s so the hub's principal-aware
 * dedup returns canonical HLCs if the first attempt did commit. The
 * cap exists to avoid pinning a permanently-offline client in a tight
 * loop; after the cap, the batch is dropped with a warn toast.
 */
const MAX_TRANSPORT_RETRIES = 5

/**
 * Per-batch result handed to `CreateOpsSubmitterOpts.onBatchResult` and
 * stored in `AppShell.batchResultHandlers`. Exported so callers
 * (cross-workspace move rollback, AppShell's handler map) don't have
 * to re-declare the union.
 */
export type BatchOutcome
  = { case: 'committed' }
    | { case: 'rejected', rejection: BatchRejection }

/**
 * createOpsSubmitter returns the batched submitter the stores call
 * after they apply a local batch speculatively. Every queued
 * `OpBatch` is collected for up to `SUBMIT_FLUSH_MS`, then sent in a
 * single `SubmitOps` request. Each `BatchResult` is dispatched back
 * to `pending.consumeBatchCommitted` / `consumeBatchRejected`, with
 * per-rejection-reason recovery driven by the plan's spec:
 *
 *   - `epoch_required` → re-bootstrap (refresh `currentEpoch`), retry
 *     the same batch (same `op_id`s — dedup-safe if it already
 *     committed under the missing epoch).
 *   - `stale_epoch` → re-bootstrap and drop the batch with a warn
 *     toast; auto-retry would defeat the staleness protection. The
 *     user re-issues the intent against current state.
 *   - any other reason → drop the batch, warn-toast keyed by reason.
 *   - transport timeout/error → retry with the same `op_id`s up to
 *     `MAX_TRANSPORT_RETRIES` times; principal-aware dedup returns
 *     the canonical HLCs if the original commit landed.
 */
export interface CreateOpsSubmitterOpts {
  orgId: () => string
  pending: () => PendingOpsManager | null
  /** Optional override for the SubmitOps client (tests). */
  client?: typeof orgCRDTClient
  /**
   * Called to force a teardown + fresh `/ws/orgevents` subscription
   * after `epoch_required` / `stale_epoch`. Resolved by the time the
   * WebSocket has been torn down; the next `OrgMaterialized` arrives
   * asynchronously and refreshes `pending.currentEpoch`.
   */
  reconnect?: () => Promise<void>
  /**
   * Per-batch hook fired immediately after each batch's outcome is
   * delivered to the pending manager (after `consumeBatchCommitted` /
   * `consumeBatchRejected`). Useful for callers that need to react to
   * specific batch ids (e.g. cross-workspace move rollback on hub
   * rejection).
   */
  onBatchResult?: (batchId: string, outcome: BatchOutcome) => void
}

export function createOpsSubmitter(opts: CreateOpsSubmitterOpts) {
  const client = opts.client ?? orgCRDTClient
  let queue: OpBatch[] = []
  let timer: ReturnType<typeof setTimeout> | undefined
  // Per-batch transport-retry counter. Cleared on commit / non-
  // transport rejection / cap hit.
  const transportRetries = new Map<string, number>()
  // Per-batch transport backoff. Each batchId carries its own delay
  // sequence (100ms → 200 → 400 → 800 → 1600 → 2000), reset on commit
  // or non-transport rejection. Repeated failures of the same batch
  // within its pending timer window are no-ops — schedule's
  // already-pending guard handles that.
  const transportBackoff = createExponentialBackoff<string>({
    initialMs: 100,
    maxMs: 2000,
    multiplier: 2,
    jitterFactor: 0,
  })

  function enqueue(batch: OpBatch): void {
    queue.push(batch)
    const pending = opts.pending()
    if (pending)
      pending.submit(batch)
    if (!timer)
      timer = setTimeout(flush, SUBMIT_FLUSH_MS)
  }

  function dropTransportRetryCounter(batchId: string): void {
    transportRetries.delete(batchId)
    transportBackoff.reset(batchId)
  }

  // Re-enqueue a batch under the same id+ops for a retry. The pending
  // manager was already notified about this batch on the original
  // `enqueue`; calling `pending.submit(batch)` again would dupe the
  // entry in `pendingBatches`. So we only push onto the wire queue.
  function rescheduleForWireRetry(batches: OpBatch[]): void {
    queue.push(...batches)
    if (!timer)
      timer = setTimeout(flush, SUBMIT_FLUSH_MS)
  }

  async function flush(): Promise<void> {
    timer = undefined
    if (queue.length === 0)
      return
    const orgId = opts.orgId()
    const pending = opts.pending()
    const epoch = pending?.state.currentEpoch ?? 0n
    const batches = queue
    queue = []
    if (!orgId || !pending)
      return
    try {
      const resp = await client.submitOps({ orgId, epoch, batches })
      let anyCommitted = false
      let needsReconnect = false
      const epochRequiredRetries: OpBatch[] = []
      for (const result of resp.results) {
        const outcome = result.outcome
        switch (outcome.case) {
          case 'committed': {
            pending.consumeBatchCommitted(result.batchId, outcome.value)
            anyCommitted = true
            dropTransportRetryCounter(result.batchId)
            opts.onBatchResult?.(result.batchId, { case: 'committed' })
            break
          }
          case 'rejected': {
            const rejection = outcome.value
            pending.consumeBatchRejected(result.batchId, rejection)
            dropTransportRetryCounter(result.batchId)
            opts.onBatchResult?.(result.batchId, { case: 'rejected', rejection })
            handleRejection(result.batchId, rejection, batches, epochRequiredRetries)
            if (
              rejection.reason === BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED
              || rejection.reason === BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH
            ) {
              needsReconnect = true
            }
            break
          }
        }
      }
      if (needsReconnect && opts.reconnect) {
        await opts.reconnect()
        // After reconnect, requeue any epoch_required batches; the
        // next flush will pick up the refreshed `currentEpoch` from
        // the new bootstrap.
        if (epochRequiredRetries.length > 0)
          rescheduleForWireRetry(epochRequiredRetries.map(cloneBatch))
      }
      // Surface a `leapmux:layout-saved` event when any batch commits,
      // so E2E tests waiting for persistence (e.g. workspace-tab moves)
      // have a deterministic signal.
      if (anyCommitted && typeof window !== 'undefined')
        window.dispatchEvent(new CustomEvent('leapmux:layout-saved'))
    }
    catch (err) {
      handleTransportFailure(pending, batches, err)
    }
  }

  function handleRejection(
    batchId: string,
    rejection: BatchRejection,
    batches: OpBatch[],
    epochRequiredRetries: OpBatch[],
  ): void {
    switch (rejection.reason) {
      case BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED: {
        const original = batches.find(b => b.batchId === batchId)
        if (original)
          epochRequiredRetries.push(original)
        break
      }
      case BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH:
        showWarnToast('Your view was offline too long. Please retry the action manually.')
        break
      default:
        showWarnToast(`Action rejected: ${rejectionLabel(rejection.reason)}`)
        break
    }
  }

  function handleTransportFailure(pending: PendingOpsManager, batches: OpBatch[], err: unknown): void {
    for (const batch of batches) {
      const attempts = (transportRetries.get(batch.batchId) ?? 0) + 1
      if (attempts > MAX_TRANSPORT_RETRIES) {
        dropTransportRetryCounter(batch.batchId)
        // Give up. Drop the batch — the speculative state stays
        // because we never received an authoritative answer; the user
        // can re-issue manually. Surface a toast so the failure isn't
        // silent.
        pending.consumeBatchRejected(batch.batchId, { $typeName: 'leapmux.v1.BatchRejection', reason: 0, offendingOpId: '' })
        showWarnToast(`Connection failed — couldn't reach the server after ${MAX_TRANSPORT_RETRIES} retries.`, err)
        continue
      }
      transportRetries.set(batch.batchId, attempts)
      // Per-batch backoff timer. `schedule` no-ops if this batchId
      // already has a pending retry — the existing timer will fire
      // and re-enqueue, no work lost.
      transportBackoff.schedule(batch.batchId, () => {
        rescheduleForWireRetry([cloneBatch(batch)])
      })
    }
  }

  onCleanup(() => {
    if (timer)
      clearTimeout(timer)
    transportBackoff.cancelAll()
  })

  return {
    enqueue,
    /** Force-flush; useful for tests + page-unload paths. */
    flush,
  }
}

export type OpsSubmitter = ReturnType<typeof createOpsSubmitter>

// cloneBatch creates a fresh OpBatch shell pointing at the same ops
// + same batch_id. Used by the retry paths so the original batches
// (potentially mutated by other code paths) stay independent.
function cloneBatch(b: OpBatch): OpBatch {
  return { ...b, ops: [...b.ops] }
}

// rejectionLabels maps every BatchRejectionReason to a user-facing
// phrase. `Record<BatchRejectionReason, string>` gives compile-time
// exhaustiveness: adding a new proto enum value lights up tsc with a
// missing-key error so the frontend can't ship a numerically-rendered
// "code 19" toast.
const rejectionLabels: Record<BatchRejectionReason, string> = {
  [BatchRejectionReason.BATCH_REJECTION_UNSPECIFIED]: 'rejected (unspecified)',
  [BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED]: 'epoch required',
  [BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH]: 'stale epoch — reconnecting',
  [BatchRejectionReason.BATCH_REJECTION_FORBIDDEN_WORKSPACE]: 'permission denied',
  [BatchRejectionReason.BATCH_REJECTION_UNKNOWN_WORKSPACE]: 'unknown workspace',
  [BatchRejectionReason.BATCH_REJECTION_TOMBSTONED_TARGET]: 'target was already deleted',
  [BatchRejectionReason.BATCH_REJECTION_OP_ID_COLLISION]: 'duplicate request',
  [BatchRejectionReason.BATCH_REJECTION_OP_ID_COLLISION_UNAUTHORIZED]: 'request collision (unauthorized)',
  [BatchRejectionReason.BATCH_REJECTION_HUB_ONLY_OP]: 'reserved operation',
  [BatchRejectionReason.BATCH_REJECTION_TAB_PLACEMENT_INVALID]: 'tab placement invalid',
  [BatchRejectionReason.BATCH_REJECTION_INCOMPLETE_RECORD]: 'incomplete record',
  [BatchRejectionReason.BATCH_REJECTION_ROOT_NODE_PROTECTED]: 'cannot delete a workspace root',
  [BatchRejectionReason.BATCH_REJECTION_ROOT_NODE_NOT_UNIQUE]: 'root node is already in use',
  [BatchRejectionReason.BATCH_REJECTION_FLOATING_MOVE_WITH_DESCENDANTS]: 'cannot move a non-empty floating window',
  [BatchRejectionReason.BATCH_REJECTION_VALUE_DOMAIN]: 'invalid value',
  [BatchRejectionReason.BATCH_REJECTION_PARENT_IMMUTABLE]: 'tile structure conflict (parent immutable)',
  [BatchRejectionReason.BATCH_REJECTION_ROOT_IMMUTABLE]: 'root assignment is immutable',
  [BatchRejectionReason.BATCH_REJECTION_TAB_ID_COLLISION_ACROSS_TYPES]: 'tab id reused across tab types',
  [BatchRejectionReason.BATCH_REJECTION_INVALID_WORKER_REF]: 'worker not available',
}

function rejectionLabel(reason: number): string {
  const known = rejectionLabels[reason as BatchRejectionReason]
  if (known !== undefined)
    return known
  // Hub running a newer enum than the frontend: render the proto name
  // (numeric TS enums support value→name reverse indexing) so logs
  // still carry a recognizable symbol, not a bare integer.
  const name = BatchRejectionReason[reason] as string | undefined
  return name ? `${name} (code ${reason})` : `code ${reason}`
}
