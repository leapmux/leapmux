import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { userCRDTClient, userCRDTUnloadClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { BatchRejectionReason, OpBatchSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { createOpsSubmitter } from './useOpsSubmitter'

// The submitter's only user-visible side effect on a permanent rejection is a
// warn toast; stub it so the tests don't spin up the toast store.
vi.mock('~/components/common/Toast', () => ({ showWarnToast: vi.fn() }))
// Default client is never used (every test injects its own), but the module
// pulls it in at import time.
vi.mock('~/api/clients', () => ({
  userCRDTClient: { submitOps: vi.fn() },
  // The unload path uses a SECOND client, over the keepalive transport: a
  // normal fetch started while the page is unloading is cancelled with it.
  userCRDTUnloadClient: { submitOps: vi.fn() },
}))

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

describe('createOpsSubmitter (retry requeue)', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('requeues a retryable batch even when no epoch refresh / reconnect is needed', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: false })
      const submitOps = vi.fn()
        .mockResolvedValueOnce(rejectedResponse('b1'))
        .mockResolvedValueOnce(committedResponse('b1'))
      const submitter = createOpsSubmitter({
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
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
        reconnect,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      // Reconnect fired to refresh the epoch, and the batch was consumed as
      // terminally rejected (non-retryable: the user re-issues against fresh
      // state). consumeBatchRejected IS the observable outcome -- it is what
      // reverts the optimistic ops.
      expect(reconnect).toHaveBeenCalledTimes(1)
      expect(pending.consumeBatchRejected).toHaveBeenCalledTimes(1)

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
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
        // No reconnect handler on purpose.
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      expect(pending.consumeBatchRejected).toHaveBeenCalledTimes(1)

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

  it('caps retryable-rejection retries, then reverts the optimistic ops and warns', async () => {
    // The give-up arm. A batch drawing a retryable rejection forever must stop
    // resending -- both to avoid hammering SubmitOps and to stop re-tearing-down
    // the /ws/userevents socket whose async bootstrap an epoch-refresh retry
    // awaits -- and it must then REVERT the optimistic ops it deliberately kept
    // applied across the retries, because the change never reached the server.
    //
    // This coverage was lost when the `onBatchResult reporting` block was deleted
    // along with that seam: only the onBatchResult assertions were obsolete, while
    // the cap and the revert-and-warn tail are still live behaviour. Nothing else
    // in this suite drives more than two attempts, and revertPendingBatch appears
    // elsewhere only as an unasserted stub.
    await createRoot(async (dispose) => {
      // Module-level mock, so it carries calls from earlier tests in this file.
      vi.mocked(showWarnToast).mockClear()
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: false })
      const submitOps = vi.fn().mockResolvedValue(rejectedResponse('b1'))
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      // Drive every backoff-scheduled retry to completion. The cap is 5 retries
      // (backoff 250,500,1000,2000,4000 -> ~7.75s), so 20s clears them all.
      await vi.advanceTimersByTimeAsync(20000)

      // 1 original attempt + 5 capped retries, then it STOPS.
      expect(submitOps).toHaveBeenCalledTimes(1 + 5)
      // ...and the kept optimistic ops are dropped exactly once, not per retry.
      expect(pending.revertPendingBatch).toHaveBeenCalledTimes(1)
      expect(pending.revertPendingBatch).toHaveBeenCalledWith('b1')
      expect(showWarnToast).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('does not revert a retryable batch before its retry commits', async () => {
    // The complement of the cap: while a retryable rejection is still being
    // retried, the optimistic ops must stay applied so the edit does not flicker
    // out and back. Reverting on the rejection and re-applying on the commit is
    // the visible bug this pins against.
    await createRoot(async (dispose) => {
      const pending = makeFakePending({ retryable: true, needsEpochRefresh: false })
      const submitOps = vi.fn()
        .mockResolvedValueOnce(rejectedResponse('b1'))
        .mockResolvedValueOnce(committedResponse('b1'))
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })
      submitter.enqueue(batch('b1'))
      await submitter.flush()
      await advancePastBackoff()

      expect(submitOps).toHaveBeenCalledTimes(2)
      expect(pending.revertPendingBatch).not.toHaveBeenCalled()
      expect(pending.consumeBatchCommitted).toHaveBeenCalled()
      dispose()
    })
  })
})

describe('createOpsSubmitter (pending manager unavailable)', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  // `CreateOpsSubmitterOpts.pending` is declared `() => PendingOpsManager | null`,
  // so the null case is part of the published contract. flush() deliberately
  // bails BEFORE draining the queue so the batches survive -- but bailing bare
  // also clears the only timer that could ever ship them. enqueue() re-arms
  // only when `timer` is unset AND it is called again, so a retained queue with
  // no further user mutation was stranded indefinitely.
  it('re-arms the flush timer while the pending manager is unavailable', async () => {
    await createRoot(async (dispose) => {
      let manager: ReturnType<typeof makeFakePending> | null = null
      const submitOps = vi.fn().mockResolvedValue(committedResponse('b1'))
      const submitter = createOpsSubmitter({
        pending: () => manager as never,
        client: { submitOps } as never,
      })
      submitter.enqueue(batch('b1'))

      // No manager yet: nothing may go out, and the queued batch must be kept.
      await vi.advanceTimersByTimeAsync(100)
      expect(submitOps).not.toHaveBeenCalled()

      // The manager becomes available with NO further enqueue. A re-armed
      // timer is the only thing that can ship the retained batch.
      manager = makeFakePending()
      await vi.advanceTimersByTimeAsync(100)
      expect(submitOps).toHaveBeenCalledTimes(1)
      const arg = submitOps.mock.calls[0]![0] as { batches: { batchId: string }[] }
      expect(arg.batches.map(b => b.batchId)).toEqual(['b1'])

      // Exactly once: the drained queue stops the re-arm loop.
      await vi.advanceTimersByTimeAsync(1000)
      expect(submitOps).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('stops re-arming once the owner is disposed', async () => {
    await createRoot(async (dispose) => {
      const submitOps = vi.fn().mockResolvedValue(committedResponse('b1'))
      const submitter = createOpsSubmitter({
        pending: () => null,
        client: { submitOps } as never,
      })
      submitter.enqueue(batch('b1'))
      await vi.advanceTimersByTimeAsync(100)
      dispose()

      await vi.advanceTimersByTimeAsync(1000)
      expect(vi.getTimerCount()).toBe(0)
      expect(submitOps).not.toHaveBeenCalled()
    })
  })
})

describe('createOpsSubmitter (op coalescing)', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  function opacityBatch(batchId: string, opId: string, windowId: string, opacity: number) {
    return create(OpBatchSchema, {
      batchId,
      ops: [{
        opId,
        body: { case: 'setFloatingWindowRegister', value: { windowId, field: { case: 'opacity', value: opacity } } },
      }],
    })
  }

  it('sends only the last write to a register and drops the superseded batches', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending()
      const submitOps = vi.fn().mockResolvedValue({ results: [] })
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })

      // A gesture-shaped burst: three writes to one register inside one window.
      submitter.enqueue(opacityBatch('b1', 'o1', 'w1', 0.9))
      submitter.enqueue(opacityBatch('b2', 'o2', 'w1', 0.8))
      submitter.enqueue(opacityBatch('b3', 'o3', 'w1', 0.7))
      await vi.advanceTimersByTimeAsync(20)

      expect(submitOps).toHaveBeenCalledTimes(1)
      const sent = submitOps.mock.calls[0]![0].batches
      expect(sent.map((b: { batchId: string }) => b.batchId)).toEqual(['b3'])
      dispose()
    })
  })

  // The integration point that makes coalescing safe. Each batch was already
  // applied speculatively at enqueue, and only a BatchResult clears a pending
  // entry -- so a batch the hub never sees must have its entry dropped here, or
  // the optimistic overlay waits on it for the life of the page.
  it('drops the pending entry of a batch that coalesced away entirely', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending()
      const submitOps = vi.fn().mockResolvedValue({ results: [] })
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })

      submitter.enqueue(opacityBatch('b1', 'o1', 'w1', 0.9))
      submitter.enqueue(opacityBatch('b2', 'o2', 'w1', 0.7))
      await vi.advanceTimersByTimeAsync(20)

      // Both were submitted speculatively; only the superseded one is reverted.
      expect(pending.submit).toHaveBeenCalledTimes(2)
      expect(pending.revertPendingBatch).toHaveBeenCalledTimes(1)
      expect(pending.revertPendingBatch).toHaveBeenCalledWith('b1')
      dispose()
    })
  })

  it('does not coalesce writes to different windows', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending()
      const submitOps = vi.fn().mockResolvedValue({ results: [] })
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })

      submitter.enqueue(opacityBatch('b1', 'o1', 'w1', 0.9))
      submitter.enqueue(opacityBatch('b2', 'o2', 'w2', 0.9))
      await vi.advanceTimersByTimeAsync(20)

      const sent = submitOps.mock.calls[0]![0].batches
      expect(sent.map((b: { batchId: string }) => b.batchId)).toEqual(['b1', 'b2'])
      expect(pending.revertPendingBatch).not.toHaveBeenCalled()
      dispose()
    })
  })
})

describe('createOpsSubmitter (parked-retry supersession)', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  function xBatch(batchId: string, opId: string, windowId: string, x: number) {
    return create(OpBatchSchema, {
      batchId,
      ops: [{
        opId,
        body: { case: 'setFloatingWindowRegister', value: { windowId, field: { case: 'x', value: x } } },
      }],
    })
  }

  // A batch parked in transport backoff has left the queue but never reached
  // the hub, so dedup will not apply when its timer fires: it commits with a
  // FRESH canonical HLC, newer than the write that superseded it, and the stale
  // mid-gesture value wins LWW on the hub and every peer. A drag that hits one
  // dropped request would land the window back where it was mid-drag.
  it('cancels a parked retry once a newer write to the same register goes out', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending()
      const submitOps = vi.fn()
        .mockRejectedValueOnce(new Error('network down'))
        .mockResolvedValue({ results: [] })
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })

      // Drag frame 1 fails at the transport layer and parks.
      submitter.enqueue(xBatch('b1', 'o1', 'w1', 0.1))
      await vi.advanceTimersByTimeAsync(20)
      expect(submitOps).toHaveBeenCalledTimes(1)

      // Drag frame 2 goes out and supersedes it.
      submitter.enqueue(xBatch('b2', 'o2', 'w1', 0.9))
      await vi.advanceTimersByTimeAsync(20)
      expect(submitOps).toHaveBeenCalledTimes(2)
      expect(pending.revertPendingBatch).toHaveBeenCalledWith('b1')

      // Well past the transport backoff: the parked batch must NOT be re-sent.
      await vi.advanceTimersByTimeAsync(5000)
      const sentBatchIds: string[] = submitOps.mock.calls.flatMap(
        c => (c[0] as { batches: { batchId: string }[] }).batches.map(b => b.batchId),
      )
      expect(sentBatchIds).toEqual(['b1', 'b2'])
      dispose()
    })
  })

  it('still retries a parked batch nothing supersedes', async () => {
    await createRoot(async (dispose) => {
      const pending = makeFakePending()
      const submitOps = vi.fn()
        .mockRejectedValueOnce(new Error('network down'))
        .mockResolvedValue({ results: [] })
      const submitter = createOpsSubmitter({
        pending: () => pending as never,
        client: { submitOps } as never,
      })

      submitter.enqueue(xBatch('b1', 'o1', 'w1', 0.1))
      await vi.advanceTimersByTimeAsync(20)

      // A write to a DIFFERENT window must not cancel it.
      submitter.enqueue(xBatch('b2', 'o2', 'w2', 0.9))
      await vi.advanceTimersByTimeAsync(20)
      expect(pending.revertPendingBatch).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(5000)
      const sentBatchIds: string[] = submitOps.mock.calls.flatMap(
        c => (c[0] as { batches: { batchId: string }[] }).batches.map(b => b.batchId),
      )
      expect(sentBatchIds).toContain('b1')
      expect(sentBatchIds.filter(id => id === 'b1')).toHaveLength(2)
      dispose()
    })
  })
})

// A `pagehide` flush that only ENQUEUES is inert on a real unload: enqueue arms
// a ~16 ms timer and the page is gone before it fires. What makes the op arrive
// is sending it over the keepalive transport, from inside the handler.
describe('createOpsSubmitter unload flush', () => {
  // Fake timers so `enqueue`'s 16 ms auto-flush cannot fire and add a call the
  // assertions here would attribute to the explicit flush under test.
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(userCRDTClient.submitOps).mockClear()
    vi.mocked(userCRDTUnloadClient.submitOps).mockClear()
  })
  afterEach(() => vi.useRealTimers())

  it('sends over the keepalive client when flushing for unload', async () => {
    const submitter = createOpsSubmitter({ pending: () => makeFakePending() as never })
    vi.mocked(userCRDTUnloadClient.submitOps).mockResolvedValue({ results: [] } as never)

    submitter.enqueue(batch('b1'))
    await submitter.flush({ unload: true })

    expect(userCRDTUnloadClient.submitOps).toHaveBeenCalledTimes(1)
    expect(userCRDTClient.submitOps).not.toHaveBeenCalled()
  })

  it('uses the ordinary client for a normal flush', async () => {
    const submitter = createOpsSubmitter({ pending: () => makeFakePending() as never })
    vi.mocked(userCRDTClient.submitOps).mockResolvedValue({ results: [] } as never)

    submitter.enqueue(batch('b1'))
    await submitter.flush()

    expect(userCRDTClient.submitOps).toHaveBeenCalledTimes(1)
    expect(userCRDTUnloadClient.submitOps).not.toHaveBeenCalled()
  })

  // keepalive shares a 64 KiB budget across every in-flight request and fails
  // outright above it, so an oversized unload flush must not be handed to it --
  // that would turn a probably-cancelled send into a guaranteed rejection.
  it('falls back to the ordinary client when the batch exceeds the keepalive budget', async () => {
    const submitter = createOpsSubmitter({ pending: () => makeFakePending() as never })
    vi.mocked(userCRDTClient.submitOps).mockResolvedValue({ results: [] } as never)

    const huge = create(OpBatchSchema, {
      batchId: 'huge',
      ops: Array.from({ length: 1000 }, () => ({})),
    })
    submitter.enqueue(huge)
    await submitter.flush({ unload: true })

    expect(userCRDTClient.submitOps).toHaveBeenCalledTimes(1)
    expect(userCRDTUnloadClient.submitOps).not.toHaveBeenCalled()
  })
})
