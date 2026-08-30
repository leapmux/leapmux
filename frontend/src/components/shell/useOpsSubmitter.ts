import type { BatchRejection, OpBatch } from '~/generated/proto/leapmux/v1/user_ops_pb'
import type { PendingOpsManager } from '~/lib/crdt'
import { onCleanup } from 'solid-js'
import { userCRDTClient, userCRDTUnloadClient } from '~/api/clients'
import { MAX_KEEPALIVE_BODY_BYTES } from '~/api/transport'
import { showWarnToast } from '~/components/common/Toast'
import { BatchRejectionReason } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { coalesceQueuedBatches, supersededParkedBatchIds } from '~/lib/crdt/coalesce'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'

const log = createLogger('opsSubmitter')

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
 * `/ws/userevents` socket whose async bootstrap it is waiting on -- when the
 * refresh never lands. After the cap the batch's optimistic ops are reverted
 * and the user is warned, because the change never reached the server.
 */
const MAX_REJECTION_RETRIES = 5

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
  pending: () => PendingOpsManager | null
  /** Optional override for the SubmitOps client (tests). */
  client?: typeof userCRDTClient
  /**
   * Optional override for the UNLOAD client (tests). Production uses the
   * keepalive transport; see `flush({ unload: true })`.
   */
  unloadClient?: typeof userCRDTClient
  /**
   * Called to force a teardown + fresh `/ws/userevents` subscription
   * after `epoch_required` / `stale_epoch`. Resolved by the time the
   * WebSocket has been torn down; the next `UserMaterialized` arrives
   * asynchronously and refreshes `pending.currentEpoch`.
   */
  reconnect?: () => Promise<void>
}

/**
 * Rough serialized size of a batch, for the keepalive budget check only.
 *
 * Deliberately an ESTIMATE: the budget is a guard rail with ~4 KiB of slack
 * below the browser's 64 KiB limit, so paying `toBinary` on the unload path to
 * learn an exact number would cost more than the precision buys.
 */
function estimateBatchBytes(batch: OpBatch): number {
  // Each op carries a handful of ids and a small value; 256 B is comfortably
  // above the measured ~39 B frame and leaves the estimate conservative.
  return 64 + batch.ops.length * 256
}

export function createOpsSubmitter(opts: CreateOpsSubmitterOpts) {
  const client = opts.client ?? userCRDTClient
  const unloadClient = opts.unloadClient ?? userCRDTUnloadClient
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
  // Batches PARKED in a retry backoff: out of `queue`, not yet re-sent, and
  // therefore invisible to coalescing. Tracked so a later flush that supersedes
  // one can cancel it -- see supersededParkedBatchIds for why a parked batch is
  // uniquely dangerous (the hub never saw it, so its retry commits with a FRESH
  // HLC that beats the newer write). Entries are removed when the timer fires,
  // when the batch reaches a terminal outcome, or when it is cancelled here.
  const parked = new Map<string, OpBatch>()
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

  /**
   * Which client this flush should use.
   *
   * Keepalive requests share a 64 KiB budget across all in-flight ones and fail
   * outright above it, so an oversized unload flush falls back to the ordinary
   * client. That send will very likely be cancelled with the page -- but a
   * cancelled request is no worse than the keepalive one the browser would have
   * refused, and the batch stays queued for the next session either way.
   */
  function submitClientFor(unload: boolean, batches: OpBatch[]): typeof client {
    if (!unload)
      return client
    const bytes = batches.reduce((sum, b) => sum + estimateBatchBytes(b), 0)
    if (bytes > MAX_KEEPALIVE_BODY_BYTES) {
      log.warn('unload flush exceeds the keepalive budget; sending without it', { bytes })
      return client
    }
    return unloadClient
  }

  function dropTransportRetryCounter(batchId: string): void {
    transportRetries.delete(batchId)
    transportBackoff.reset(batchId)
    parked.delete(batchId)
  }

  function dropRejectionRetryCounter(batchId: string): void {
    rejectionRetries.delete(batchId)
    rejectionBackoff.reset(batchId)
    parked.delete(batchId)
  }

  // Re-enqueue a batch under the same id+ops for a retry. The pending manager
  // was already notified about this batch on the original `enqueue`; calling
  // `pending.submit(batch)` again would dupe the entry in `pendingBatches`. So
  // we only push onto the wire queue.
  //
  // Caveat when the retry follows a RECONNECT: if that reconnect fell back to a
  // full snapshot, `bootstrap()` cleared `pendingBatches` (the snapshot is the
  // hub's authoritative view and supersedes speculative edits made against the
  // state it replaced). The optimistic overlay is then gone until the hub's
  // broadcast echo of the retried batch lands, so the edit visibly reverts and
  // re-applies. Correctness is preserved -- `consumeRemote` applies the echoed
  // ops unconditionally -- but `revertPendingBatch` becomes a no-op for such a
  // batch, since there is no longer a pending entry to revert.
  function rescheduleForWireRetry(batches: OpBatch[]): void {
    queue.push(...batches)
    if (!timer)
      timer = setTimeout(flush, SUBMIT_FLUSH_MS)
  }

  /**
   * Park `batch` and arm `backoff` to un-park and re-enqueue it.
   *
   * The rejection-retry and transport-retry paths differ ONLY in which backoff
   * they use, and both must keep `parked` in exact lockstep with the scheduled
   * timer: `supersededParkedBatchIds` reads that map to cancel a stale retry
   * whose fresh canonical HLC would otherwise beat a newer write. Two hand-copied
   * sequences meant that correctness-critical bookkeeping had two homes.
   *
   * `schedule` no-ops if this batchId already has a pending retry -- the
   * existing timer fires and re-enqueues, so no work is lost.
   */
  function scheduleParkedRetry(backoff: ReturnType<typeof createExponentialBackoff<string>>, batch: OpBatch): void {
    parked.set(batch.batchId, batch)
    backoff.schedule(batch.batchId, () => {
      parked.delete(batch.batchId)
      rescheduleForWireRetry([cloneBatch(batch)])
    })
  }

  async function flush(flushOpts?: { unload?: boolean }): Promise<void> {
    timer = undefined
    if (queue.length === 0)
      return
    const pending = opts.pending()
    // Bail BEFORE draining `queue`: an early return taken after the
    // drain would throw away already-queued batches with no path to
    // resubmit them.
    //
    // Re-arm on the way out. `pending` is declared nullable, so keeping the
    // queue is only half a promise: `timer` was cleared above, and `enqueue`
    // arms it only when it is called again — so bailing bare strands every
    // retained batch until the user happens to make another CRDT mutation.
    // The re-arm polls the aggregator window until a manager shows up;
    // `onCleanup` clears the timer on dispose, so it cannot outlive the owner.
    if (!pending) {
      timer = setTimeout(flush, SUBMIT_FLUSH_MS)
      return
    }
    const epoch = pending.state.currentEpoch
    // Merge same-register writes that piled up in this window. High-frequency
    // gestures emit the same LWW register repeatedly, and only the last write
    // can survive the merge anywhere, so the earlier ones are pure wire,
    // storage and fan-out cost. See coalesceQueuedBatches for the two
    // structural rules that keep this behaviour-preserving.
    const { batches, droppedBatchIds, droppedOps } = coalesceQueuedBatches(queue)
    queue = []
    // A batch that coalesced away entirely is never sent, so no BatchResult
    // will ever arrive to clear its pending entry. Drop it here or the
    // optimistic overlay waits on it forever. Reverting is safe precisely
    // because every op it held was superseded by one still being sent.
    for (const batchId of droppedBatchIds) {
      pending.revertPendingBatch(batchId)
      dropTransportRetryCounter(batchId)
      dropRejectionRetryCounter(batchId)
    }
    if (droppedOps > 0)
      log.debug('coalesced superseded ops before submit', { droppedOps, droppedBatches: droppedBatchIds.length })
    // A batch parked in a retry backoff is older than this flush but has not
    // reached the hub, so its eventual retry would commit with a canonical HLC
    // NEWER than what we are about to send and win LWW with a stale value.
    // Cancel any whose every register this flush rewrites.
    for (const batchId of supersededParkedBatchIds(parked.values(), batches)) {
      dropTransportRetryCounter(batchId)
      dropRejectionRetryCounter(batchId)
      pending.revertPendingBatch(batchId)
      log.debug('cancelled a parked retry superseded by a newer write', { batchId })
    }
    if (batches.length === 0)
      return
    try {
      const resp = await submitClientFor(flushOpts?.unload === true, batches).submitOps({ epoch, batches })
      let anyCommitted = false
      let needsReconnect = false
      const retryableRejections: { batch: OpBatch, needsEpochRefresh: boolean }[] = []
      for (const result of resp.results) {
        const outcome = result.outcome
        switch (outcome.case) {
          case 'committed': {
            pending.consumeBatchCommitted(result.batchId, outcome.value)
            anyCommitted = true
            dropTransportRetryCounter(result.batchId)
            dropRejectionRetryCounter(result.batchId)
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
              // requeued below, and its optimistic ops stay applied so the edit
              // does not flicker out and back. Reverting here and then committing
              // on the retry would be the visible bug. (The one case where the
              // overlay does not survive is a reconnect that full-snapshots in
              // between -- see rescheduleForWireRetry.)
              const original = batches.find(b => b.batchId === result.batchId)
              if (original)
                retryableRejections.push({ batch: original, needsEpochRefresh })
            }
            else {
              // Permanent rejection: this IS the final outcome. Clear any retry
              // state (a batch retryable on an earlier attempt may now be
              // terminally rejected) and warn the user.
              dropRejectionRetryCounter(result.batchId)
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
      for (const { batch, needsEpochRefresh } of retryableRejections) {
        if (needsEpochRefresh && !opts.reconnect) {
          // Give up: no reconnect handler to refresh the epoch. consumeBatchRejected
          // KEPT this batch's optimistic ops applied (it was retryable); now that
          // it is terminal, revert them so the UI doesn't leave the edit stuck
          // visible.
          pending.revertPendingBatch(batch.batchId)
          dropRejectionRetryCounter(batch.batchId)
          showWarnToast('Couldn\'t sync your change (your view needs to reconnect). Please retry the action manually.')
          continue
        }
        const attempts = (rejectionRetries.get(batch.batchId) ?? 0) + 1
        if (attempts > MAX_REJECTION_RETRIES) {
          // Exhausted retries: give up. The optimistic ops were KEPT applied
          // across the retries (retryable rejection); revert them now.
          pending.revertPendingBatch(batch.batchId)
          dropRejectionRetryCounter(batch.batchId)
          showWarnToast(`Couldn't sync your change after ${MAX_REJECTION_RETRIES} attempts. Please retry the action manually.`)
          continue
        }
        rejectionRetries.set(batch.batchId, attempts)
        scheduleParkedRetry(rejectionBackoff, batch)
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
        // the user re-issues manually. Note this is NOT an authoritative
        // rejection: nothing may treat a transport give-up as the server having
        // refused the change. Surface a toast so the failure isn't silent.
        pending.consumeBatchRejected(batch.batchId, { $typeName: 'leapmux.v1.BatchRejection', reason: 0, offendingOpId: '' })
        showWarnToast(`Connection failed — couldn't reach the server after ${MAX_TRANSPORT_RETRIES} retries.`, err)
        continue
      }
      transportRetries.set(batch.batchId, attempts)
      scheduleParkedRetry(transportBackoff, batch)
    }
  }

  onCleanup(() => {
    if (timer)
      clearTimeout(timer)
    transportBackoff.cancelAll()
    rejectionBackoff.cancelAll()
    parked.clear()
  })

  return {
    enqueue,
    /**
     * Force-flush.
     *
     * `{ unload: true }` routes the request through the keepalive transport so
     * it survives the document being torn down. A normal fetch started from a
     * `pagehide` handler is cancelled with the page, which is why simply
     * enqueueing at unload was never enough: the 16 ms timer never fires.
     */
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
