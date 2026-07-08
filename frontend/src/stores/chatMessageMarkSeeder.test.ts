import type { listMessageMarks } from '~/api/workerRpc'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { ListMessageMarksResponseSchema, MarkType, MessageMarkSchema } from '~/generated/leapmux/v1/agent_pb'
import { createMessageMarksStore } from '~/stores/chatMessageMarks'
import { createMessageMarkSeeder, MAX_MESSAGE_MARK_SEED_RESCHEDULES } from '~/stores/chatMessageMarkSeeder'

// Unit tests for the seed-race machine itself, driven directly against a FAKE
// ListMessageMarks RPC + a REAL createMessageMarksStore (no mocked module, no whole
// chat store). The store's chat.store.test.ts seed tests exercise the SAME machine
// end-to-end through the delegating store.loadMessageMarks; these pin its retry /
// revision-race / cancellation edges close to the source.

/** A ListMessageMarks response; pass `undefined` min/max for an indeterminate seq range. */
function marksResponse(seqs: bigint[], minSeq: bigint | undefined, maxSeq: bigint | undefined) {
  return create(ListMessageMarksResponseSchema, {
    marks: seqs.map(seq => create(MessageMarkSchema, { seq, type: MarkType.USER_MESSAGE })),
    minSeq,
    maxSeq,
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** A fresh seeder over a real marks store with the RPC replaced by a controllable fake. */
function harness() {
  const rpc = vi.fn<typeof listMessageMarks>()
  const marks = createMessageMarksStore()
  const seeder = createMessageMarkSeeder({ listMessageMarks: rpc, marks })
  return { rpc, marks, seeder }
}

describe('chatmessagemarkseeder', () => {
  it('retries a first seed whose range stays indeterminate, then gives up after the bounded reschedules', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const { rpc, marks, seeder } = harness()
      // Every response carries marks but an indeterminate (unset) seq range, so seed() keeps
      // loaded=false. Without a cap this would reschedule a fetch every retry-delay forever.
      rpc.mockResolvedValue(marksResponse([3n], undefined, undefined))

      await seeder.load('w1', 'a1')
      expect(rpc).toHaveBeenCalledTimes(1)
      expect(marks.get('a1').loaded).toBe(false)
      expect(warn).toHaveBeenCalledWith('message marks seed retry scheduled', {
        agentId: 'a1',
        reason: 'first seed range indeterminate',
        rescheduleDepth: 0,
      })

      // Drive far more reschedule ticks than the cap allows; the chain must terminate.
      for (let i = 0; i < 20; i++) {
        await vi.runOnlyPendingTimersAsync()
        await Promise.resolve()
      }

      // Bounded to MAX_MESSAGE_MARK_SEED_RESCHEDULES fetches total, then it gives up.
      expect(rpc).toHaveBeenCalledTimes(MAX_MESSAGE_MARK_SEED_RESCHEDULES)
      expect(marks.get('a1').loaded).toBe(false)
      expect(warn).toHaveBeenCalledWith('message marks seed gave up after retries', expect.objectContaining({
        agentId: 'a1',
        reason: 'first seed range indeterminate',
      }))
      // No pending timer remains -- the poll has genuinely stopped, not just paused.
      expect(vi.getTimerCount()).toBe(0)
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('re-fetches when a live mark change races the in-flight seed instead of seeding the stale snapshot', async () => {
    const { rpc, marks, seeder } = harness()
    const inflight = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    rpc
      .mockReturnValueOnce(inflight.promise)
      .mockResolvedValueOnce(marksResponse([2n], 2n, 2n))

    const load = seeder.load('w1', 'a1')
    // A live mark lands while the FIRST snapshot is in flight, bumping the marks store's
    // live-mutation revision. The seeder must discard the (now-stale) snapshot and re-fetch
    // rather than seed the empty pre-mark response and drop the dot.
    marks.noteMark('a1', 2n, MarkType.USER_MESSAGE)
    inflight.resolve(marksResponse([], 0n, 0n))
    await load

    expect(rpc).toHaveBeenCalledTimes(2)
    const data = marks.get('a1')
    expect(data.loaded).toBe(true)
    expect(data.marks.map(m => m.seq)).toEqual([2n])
  })

  it('stops the retry chain when the watch signal is aborted after a retry was scheduled', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const { rpc, marks, seeder } = harness()
      const controller = new AbortController()
      // An indeterminate first seed schedules a retry timer.
      rpc.mockResolvedValue(marksResponse([3n], undefined, undefined))

      await seeder.load('w1', 'a1', controller.signal)
      expect(rpc).toHaveBeenCalledTimes(1)
      expect(vi.getTimerCount()).toBe(1)

      // The subscription tears down before the retry fires: the scheduled timer's callback
      // must bail (its watchSignal guard) rather than re-issue the seed against a worker the
      // reader navigated away from -- and no fresh timer is armed.
      controller.abort()
      await vi.runOnlyPendingTimersAsync()

      expect(rpc).toHaveBeenCalledTimes(1)
      expect(vi.getTimerCount()).toBe(0)
      expect(marks.get('a1').loaded).toBe(false)
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('a reseed with an empty workerId stops the pending retry chain (the worker is gone)', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const { rpc, seeder } = harness()
      // A VALID first seed comes back indeterminate and arms a retry timer.
      rpc.mockResolvedValue(marksResponse([3n], undefined, undefined))
      await seeder.load('w1', 'a1')
      expect(rpc).toHaveBeenCalledTimes(1)
      expect(vi.getTimerCount()).toBe(1)

      // A reseed with an EMPTY workerId means the agent's worker/tab is gone (getAgentTab returned
      // undefined). That is the deliberate signal to STOP retrying -- the retry chain is cancelled
      // and nothing is rescheduled, so the seeder never polls a worker the reader can't reach.
      await seeder.load('', 'a1')
      expect(vi.getTimerCount()).toBe(0) // the pending retry was cancelled
      await vi.runOnlyPendingTimersAsync()
      expect(rpc).toHaveBeenCalledTimes(1) // ...and never re-issued
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('cancels a pending retry timer when the agent is forgotten', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const { rpc, marks, seeder } = harness()
      // An indeterminate first seed schedules a retry timer.
      rpc.mockResolvedValue(marksResponse([3n], undefined, undefined))

      await seeder.load('w1', 'a1')
      expect(rpc).toHaveBeenCalledTimes(1)
      expect(vi.getTimerCount()).toBe(1)

      // Closing the agent cancels the pending retry outright (clearTimeout), not merely
      // guarding it -- so the timer is gone immediately and never re-issues the seed.
      seeder.forget('a1')
      expect(vi.getTimerCount()).toBe(0)

      await vi.runOnlyPendingTimersAsync()
      expect(rpc).toHaveBeenCalledTimes(1)
      expect(marks.get('a1').loaded).toBe(false)
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })
})
