import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BatchRejectionReason, OpBatchSchema } from '~/generated/leapmux/v1/org_ops_pb'
import { createOpsSubmitter } from './useOpsSubmitter'

// The submitter's only user-visible side effect on a permanent rejection is a
// warn toast; stub it so the tests don't spin up the toast store.
vi.mock('~/components/common/Toast', () => ({ showWarnToast: vi.fn() }))
// Default client is never used (every test injects its own), but the module
// pulls it in at import time.
vi.mock('~/api/clients', () => ({ orgCRDTClient: { submitOps: vi.fn() } }))

function batch(id: string) {
  return create(OpBatchSchema, { batchId: id, ops: [] })
}

interface FakePendingOpts {
  retryable?: boolean
  needsEpochRefresh?: boolean
}

// A faithful stand-in for PendingOpsManager as seen by the submitter: it owns
// the rejection classification (`retryable`, `needsEpochRefresh`), which is
// exactly the contract the submitter must honor. Controlling the classification
// lets us exercise a retryable-but-no-refresh reason (the forward-compat case
// the allowlist exists to serve) that today's RETRYABLE_REJECTIONS can't
// produce on its own.
function makeFakePending(opts: FakePendingOpts = {}) {
  return {
    state: { currentEpoch: 5n },
    submit: vi.fn(),
    consumeBatchCommitted: vi.fn(),
    consumeBatchRejected: vi.fn(() => ({
      reason: BatchRejectionReason.BATCH_REJECTION_FORBIDDEN_WORKSPACE,
      offendingOpId: '',
      retryable: opts.retryable ?? false,
      needsEpochRefresh: opts.needsEpochRefresh ?? false,
    })),
    // Called by the submitter to drop a kept retryable batch when it finally
    // gives up (retry cap or no reconnect handler).
    revertPendingBatch: vi.fn(),
  }
}

function rejectedResponse(batchId: string) {
  return {
    results: [{
      batchId,
      outcome: {
        case: 'rejected' as const,
        value: { $typeName: 'leapmux.v1.BatchRejection', reason: BatchRejectionReason.BATCH_REJECTION_FORBIDDEN_WORKSPACE, offendingOpId: '' },
      },
    }],
  }
}

function committedResponse(batchId: string) {
  return {
    results: [{
      batchId,
      outcome: { case: 'committed' as const, value: { committed: [], epoch: 5n } },
    }],
  }
}

// A retryable rejection is now requeued through an exponential backoff (250ms
// base) rather than the bare aggregator window, so tests drive the retry by
// advancing fake timers past the backoff + the 16ms flush window. This spans a
// few retries at most, well under maxMs.
async function advancePastBackoff() {
  await vi.advanceTimersByTimeAsync(300)
}

describe('createopssubmitter (retry requeue)', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('requeues a retryable batch even when no epoch refresh / reconnect is needed', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: false })
      const submitOps = vi.fn()
        .mockResolvedValueOnce(rejectedResponse('b1'))
        .mockResolvedValueOnce(committedResponse('b1'))
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        // No reconnect handler on purpose: requeue must be driven by the
        // `retryable` allowlist, NOT by a needed reconnect.
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      expect(submitOps).toHaveBeenCalledTimes(1)

      // The batch was retryable, so it must be requeued (the pre-fix behaviour
      // silently dropped it) — now through the backoff, not a bare 16ms timer.
      await advancePastBackoff()
      expect(submitOps).toHaveBeenCalledTimes(2)
      const secondArg = submitOps.mock.calls[1]![0] as { batches: { batchId: string }[] }
      expect(secondArg.batches.map(b => b.batchId)).toEqual(['b1'])
      dispose()
    })
  })

  it('reconnects before requeueing when the rejection needs an epoch refresh', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: true })
      const submitOps = vi.fn()
        .mockResolvedValueOnce(rejectedResponse('b1'))
        .mockResolvedValueOnce(committedResponse('b1'))
      const reconnect = vi.fn(async () => {})
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        reconnect,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      expect(reconnect).toHaveBeenCalledTimes(1)

      await advancePastBackoff()
      expect(submitOps).toHaveBeenCalledTimes(2)
      const secondArg = submitOps.mock.calls[1]![0] as { batches: { batchId: string }[] }
      expect(secondArg.batches.map(b => b.batchId)).toEqual(['b1'])
      dispose()
    })
  })

  it('does not requeue a permanent (non-retryable) rejection', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: false, needsEpochRefresh: false })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const reconnect = vi.fn(async () => {})
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        reconnect,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()

      await advancePastBackoff()
      expect(submitOps).toHaveBeenCalledTimes(1)
      expect(reconnect).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('reconnects (but never requeues) a non-retryable rejection that needs an epoch refresh', async () => {
    // The stale_epoch shape: NOT retryable (the user re-issues), but it MUST
    // still re-bootstrap so `currentEpoch` advances -- otherwise the client stays
    // pinned at its stale epoch and every manual retry re-rejects forever. The
    // pre-fix code gated the reconnect on `retryable`, so this reconnect was lost
    // and the batch's `needsEpochRefresh: true` classification was dead.
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: false, needsEpochRefresh: true })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const reconnect = vi.fn(async () => {})
      const onBatchResult = vi.fn()
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        reconnect,
        onBatchResult,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      // Reconnect fired to refresh the epoch, and the batch was reported
      // terminally rejected (non-retryable: the user re-issues against fresh state).
      expect(reconnect).toHaveBeenCalledTimes(1)
      expect(onBatchResult).toHaveBeenCalledTimes(1)
      expect(onBatchResult).toHaveBeenCalledWith('b1', expect.objectContaining({ case: 'rejected' }))

      // It must NOT auto-retry -- a stale_epoch batch is never requeued.
      await advancePastBackoff()
      expect(submitOps).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('reports a non-retryable epoch-refresh rejection terminally when no reconnect handler is wired', async () => {
    // Edge case for the stale_epoch shape with the optional reconnect handler
    // absent: needsReconnect is set (needsEpochRefresh: true) but opts.reconnect
    // is undefined, so the `needsReconnect && opts.reconnect` guard must short-
    // circuit without throwing. The batch is still reported terminally rejected
    // and never requeued (non-retryable).
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: false, needsEpochRefresh: true })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const onBatchResult = vi.fn()
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        onBatchResult,
        // No reconnect handler on purpose.
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      expect(onBatchResult).toHaveBeenCalledTimes(1)
      expect(onBatchResult).toHaveBeenCalledWith('b1', expect.objectContaining({ case: 'rejected' }))

      await advancePastBackoff()
      expect(submitOps).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('skips requeue when an epoch refresh is required but no reconnect handler is wired', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: true })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        // No reconnect handler: a batch that needs an epoch refresh can't make
        // progress, so requeuing it would loop -- it must be dropped.
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()

      await advancePastBackoff()
      expect(submitOps).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('requeues a no-refresh retryable batch even when a sibling in the same response needs an epoch refresh and no reconnect is wired', async () => {
    // Regression guard: the requeue gate is PER BATCH, not a response-wide OR.
    // b-refresh needs an epoch refresh (can't progress without a reconnect) while
    // b-plain is retryable with NO refresh needed. A response-wide gate would drop
    // BOTH because b-refresh set the shared "needs reconnect" flag; the per-batch
    // gate must still requeue b-plain.
    await createRoot(async (dispose) => {
      const rejection = {
        $typeName: 'leapmux.v1.BatchRejection' as const,
        reason: BatchRejectionReason.BATCH_REJECTION_FORBIDDEN_WORKSPACE,
        offendingOpId: '',
      }
      const pending = {
        state: { currentEpoch: 5n },
        submit: vi.fn(),
        consumeBatchCommitted: vi.fn(),
        consumeBatchRejected: vi.fn((batchId: string) => ({
          reason: BatchRejectionReason.BATCH_REJECTION_FORBIDDEN_WORKSPACE,
          offendingOpId: '',
          retryable: true,
          needsEpochRefresh: batchId === 'b-refresh',
        })),
        revertPendingBatch: vi.fn(),
      }
      const submitOps = vi.fn()
        .mockResolvedValueOnce({ results: [
          { batchId: 'b-refresh', outcome: { case: 'rejected' as const, value: rejection } },
          { batchId: 'b-plain', outcome: { case: 'rejected' as const, value: rejection } },
        ] })
        .mockResolvedValueOnce(committedResponse('b-plain'))
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        // No reconnect handler on purpose.
      })
      submitter.enqueue(batch('b-refresh'))
      submitter.enqueue(batch('b-plain'))
      await submitter.flush()
      expect(submitOps).toHaveBeenCalledTimes(1)

      await advancePastBackoff()
      // Only b-plain requeues; b-refresh is dropped (no reconnect to refresh epoch).
      expect(submitOps).toHaveBeenCalledTimes(2)
      const secondArg = submitOps.mock.calls[1]![0] as { batches: { batchId: string }[] }
      expect(secondArg.batches.map(b => b.batchId)).toEqual(['b-plain'])
      dispose()
    })
  })
})

describe('createopssubmitter (onBatchResult reporting)', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('does NOT report a retryable rejection until it commits, then reports committed', async () => {
    // Guards the cross-workspace-move rollback: a retryable rejection that later
    // commits must not be surfaced as `rejected`, or the rollback reverses the
    // worker while the retry lands, diverging worker + CRDT.
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: false })
      const submitOps = vi.fn()
        .mockResolvedValueOnce(rejectedResponse('b1'))
        .mockResolvedValueOnce(committedResponse('b1'))
      const onBatchResult = vi.fn()
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        onBatchResult,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      // First attempt was a retryable rejection: nothing reported yet.
      expect(onBatchResult).not.toHaveBeenCalled()

      await advancePastBackoff()
      // Retry committed: the ONLY reported outcome is 'committed'.
      expect(onBatchResult).toHaveBeenCalledTimes(1)
      expect(onBatchResult).toHaveBeenCalledWith('b1', { case: 'committed' })
      dispose()
    })
  })

  it('reports a permanent rejection immediately as rejected', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: false, needsEpochRefresh: false })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const onBatchResult = vi.fn()
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        onBatchResult,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      expect(onBatchResult).toHaveBeenCalledTimes(1)
      expect(onBatchResult).toHaveBeenCalledWith('b1', expect.objectContaining({ case: 'rejected' }))
      dispose()
    })
  })

  it('caps retryable-rejection retries and reports an authoritative rejection on give-up', async () => {
    // A batch that keeps drawing a retryable rejection must not resend forever
    // (nor keep tearing down the socket whose bootstrap it awaits). After the
    // cap it is reported once as rejected so a registered handler can react.
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: false })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const onBatchResult = vi.fn()
      const submitter = createOpsSubmitter({
        orgId: () => 'org-1',
        pending: () => pending as never,
        client: { submitOps } as never,
        onBatchResult,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      // Drive every backoff-scheduled retry to completion. The cap is 5 retries
      // (backoff 250,500,1000,2000,4000 -> ~7.75s), so 20s clears them all.
      await vi.advanceTimersByTimeAsync(20000)

      // 1 original attempt + 5 capped retries = 6 SubmitOps, then it stops.
      expect(submitOps).toHaveBeenCalledTimes(1 + 5)
      // The give-up reports the batch as an authoritative rejection exactly once.
      expect(onBatchResult).toHaveBeenCalledTimes(1)
      expect(onBatchResult).toHaveBeenCalledWith('b1', expect.objectContaining({ case: 'rejected' }))
      dispose()
    })
  })
})
