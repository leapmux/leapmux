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
 * MAX_REJECTION_RETRIES caps auto-retries of a hub-REJECTED batch whose
 * reason is retryable (e.g. `epoch_required` after an epoch bump). Like
 * the transport cap it stops a client from re-hammering SubmitOps in a
 * tight loop -- and, for an epoch-refresh reason, from re-tearing-down the
 * `/ws/orgevents` socket whose async bootstrap it is waiting on -- when the
 * refresh never lands. After the cap the batch is reported as an
 * authoritative rejection so its caller (e.g. the cross-workspace-move
 * rollback) can react, with a warn toast.
 */
const MAX_REJECTION_RETRIES = 5

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
  // Per-batch retryable-rejection counter, cleared on commit / give-up.
  const rejectionRetries = new Map<string, number>()
  // Per-batch retryable-rejection backoff. A retryable rejection that needs
  // an epoch refresh must give reconnect()'s ASYNC bootstrap time to refresh
  // `currentEpoch` before the resend: the aggregator window (16ms) is far too
  // short, so a fixed-cadence requeue would resend with the stale epoch and
  // reconnect() would tear down the in-flight bootstrap each round (a
  // self-starving loop). The backoff (250ms -> 4s) lets the refresh win the
  // race; the cap bounds the worst case. Reset on commit / non-retryable
  // rejection / cap hit.
  const rejectionBackoff = createExponentialBackoff<string>({
    initialMs: 250,
    maxMs: 4000,
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

  function dropRejectionRetryCounter(batchId: string): void {
    rejectionRetries.delete(batchId)
    rejectionBackoff.reset(batchId)
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
      const retryableRejections: { batch: OpBatch, rejection: BatchRejection, needsEpochRefresh: boolean }[] = []
      for (const result of resp.results) {
        const outcome = result.outcome
        switch (outcome.case) {
          case 'committed': {
            pending.consumeBatchCommitted(result.batchId, outcome.value)
            anyCommitted = true
            dropTransportRetryCounter(result.batchId)
            dropRejectionRetryCounter(result.batchId)
            opts.onBatchResult?.(result.batchId, { case: 'committed' })
            break
          }
          case 'rejected': {
            const rejection = outcome.value
            // Both rejection classifications come from consumeBatchRejected (the
            // module that owns them): `retryable` is the single source of truth for
            // auto-retry eligibility (pendingOps' fail-safe allowlist), and
            // `needsEpochRefresh` is the single source of truth for "a reconnect
            // must refresh currentEpoch first". Neither is re-decided by a
            // drift-prone switch here.
            const { retryable, needsEpochRefresh } = pending.consumeBatchRejected(result.batchId, rejection)
            dropTransportRetryCounter(result.batchId)
            // A rejection that needs an epoch refresh (`epoch_required` OR
            // `stale_epoch`) must re-bootstrap so `currentEpoch` advances --
            // otherwise a `stale_epoch` client (NOT retryable, so never requeued
            // below) stays pinned at its stale epoch: the user's "retry manually"
            // re-submits the same epoch and re-rejects forever. This is orthogonal
            // to `retryable`, so it is gated on `needsEpochRefresh` alone, not on
            // the retryable branch -- `stale_epoch` reconnects but does NOT
            // auto-retry (the user re-issues), while `epoch_required` reconnects
            // AND auto-retries.
            if (needsEpochRefresh)
              needsReconnect = true
            if (retryable) {
              // A retryable rejection is NOT a final outcome -- the batch is
              // requeued below. Do NOT report it to onBatchResult yet: a handler
              // (e.g. the cross-workspace-move rollback) would reverse its
              // optimistic action while the retry is still in flight and then the
              // retry commits, permanently diverging the worker and the CRDT. The
              // authoritative outcome is reported on commit, or on retry give-up.
              const original = batches.find(b => b.batchId === result.batchId)
              if (original)
                retryableRejections.push({ batch: original, rejection, needsEpochRefresh })
            }
            else {
              // Permanent rejection: this IS the final outcome. Clear any retry
              // state (a batch retryable on an earlier attempt may now be
              // terminally rejected), report it, and warn the user.
              dropRejectionRetryCounter(result.batchId)
              opts.onBatchResult?.(result.batchId, { case: 'rejected', rejection })
              showRejectionToast(rejection)
            }
            break
          }
        }
      }
      if (needsReconnect && opts.reconnect)
        await opts.reconnect()
      // Requeue each retryable batch under a capped exponential backoff, gating
      // PER BATCH rather than on a response-wide flag. A batch that needs an epoch
      // refresh but has no reconnect handler to provide one can't make progress
      // (requeuing without a fresh epoch would just re-reject), so it's dropped
      // with an authoritative rejection. Every OTHER retryable batch requeues --
      // including a no-refresh-needed one that merely shared a response with a
      // refresh-needing sibling (the forward-compat case the allowlist exists to
      // serve): a response-wide OR here would silently drop it. A refresh-needing
      // batch has already had its reconnect() awaited above.
      for (const { batch, rejection, needsEpochRefresh } of retryableRejections) {
        if (needsEpochRefresh && !opts.reconnect) {
          // Give up: no reconnect handler to refresh the epoch. consumeBatchRejected
          // KEPT this batch's optimistic ops applied (it was retryable); now that it
          // is terminal, revert them before reporting so the UI doesn't leave the
          // edit stuck visible.
          pending.revertPendingBatch(batch.batchId)
          dropRejectionRetryCounter(batch.batchId)
          opts.onBatchResult?.(batch.batchId, { case: 'rejected', rejection })
          showWarnToast('Couldn\'t sync your change (your view needs to reconnect). Please retry the action manually.')
          continue
        }
        const attempts = (rejectionRetries.get(batch.batchId) ?? 0) + 1
        if (attempts > MAX_REJECTION_RETRIES) {
          // Exhausted retries: give up. The optimistic ops were KEPT applied across
          // the retries (retryable rejection); revert them now, then report the
          // authoritative rejection so a registered handler (e.g. the
          // cross-workspace-move rollback) can reverse its optimistic action.
          pending.revertPendingBatch(batch.batchId)
          dropRejectionRetryCounter(batch.batchId)
          opts.onBatchResult?.(batch.batchId, { case: 'rejected', rejection })
          showWarnToast(`Couldn't sync your change after ${MAX_REJECTION_RETRIES} attempts. Please retry the action manually.`)
          continue
        }
        rejectionRetries.set(batch.batchId, attempts)
        rejectionBackoff.schedule(batch.batchId, () => rescheduleForWireRetry([cloneBatch(batch)]))
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

  // showRejectionToast surfaces a permanent (non-retryable) rejection to the
  // user. A retryable rejection recovers on its own (requeue) and gets no toast
  // unless it later exhausts its retries.
  function showRejectionToast(rejection: BatchRejection): void {
    switch (rejection.reason) {
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
        // Give up. consumeBatchRejected splices this still-pending batch and
        // recomputes speculative WITHOUT it, so its optimistic ops are
        // reverted (not preserved) — we never got an authoritative answer, so
        // the user re-issues manually. We deliberately do NOT call
        // opts.onBatchResult here: a transport give-up is not an authoritative
        // rejection, so a cross-workspace-move rollback must not reverse the
        // worker off it. Surface a toast so the failure isn't silent.
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
    rejectionBackoff.cancelAll()
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
