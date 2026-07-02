import { describe, expect, it } from 'vitest'
import { createWorkerPriorityGate } from './workerPriorityGate'

/** A job whose completion the test controls. */
function makeJob(startedLog: string[], name: string) {
  let release!: () => void
  const releasePromise = new Promise<void>((r) => {
    release = r
  })
  const work = (): Promise<string> => {
    startedLog.push(name)
    return releasePromise.then(() => name)
  }
  return { work, release }
}

describe('workerprioritygate', () => {
  it('dispatches immediately while slots are free, queueing the rest FIFO', async () => {
    const gate = createWorkerPriorityGate(2)
    const started: string[] = []
    const a = makeJob(started, 'a')
    const b = makeJob(started, 'b')
    const c = makeJob(started, 'c')
    const pa = gate.enqueue(a.work)
    const pb = gate.enqueue(b.work)
    const pc = gate.enqueue(c.work)
    expect(started).toEqual(['a', 'b']) // two slots
    expect(gate.queuedCount()).toBe(1)

    a.release()
    await pa
    expect(started).toEqual(['a', 'b', 'c'])
    b.release()
    c.release()
    await Promise.all([pb, pc])
    expect(gate.queuedCount()).toBe(0)
  })

  it('a high-priority job preempts queued low-priority ones', async () => {
    const gate = createWorkerPriorityGate(1)
    const started: string[] = []
    const first = makeJob(started, 'first')
    const low1 = makeJob(started, 'low1')
    const low2 = makeJob(started, 'low2')
    const high = makeJob(started, 'high')
    const all = [
      gate.enqueue(first.work), // occupies the slot
      gate.enqueue(low1.work, () => true),
      gate.enqueue(low2.work, () => true),
      gate.enqueue(high.work), // enqueued LAST but high priority
    ]
    first.release()
    await new Promise(r => setTimeout(r))
    expect(started).toEqual(['first', 'high'])

    high.release()
    await new Promise(r => setTimeout(r))
    expect(started).toEqual(['first', 'high', 'low1']) // then FIFO among lows
    low1.release()
    low2.release()
    await Promise.all(all)
  })

  it('re-evaluates priority at dequeue time (an offscreen row scrolled into view upgrades)', async () => {
    const gate = createWorkerPriorityGate(1)
    const started: string[] = []
    const first = makeJob(started, 'first')
    const stillLow = makeJob(started, 'stillLow')
    const upgraded = makeJob(started, 'upgraded')
    let upgradedIsLow = true
    const all = [
      gate.enqueue(first.work),
      gate.enqueue(stillLow.work, () => true),
      gate.enqueue(upgraded.work, () => upgradedIsLow),
    ]
    // The row scrolls into the viewport while its job is still queued.
    upgradedIsLow = false
    first.release()
    await new Promise(r => setTimeout(r))
    expect(started).toEqual(['first', 'upgraded'])
    upgraded.release()
    stillLow.release()
    await Promise.all(all)
  })

  it('passes through resolutions, rejections, and synchronous throws, releasing the slot', async () => {
    const gate = createWorkerPriorityGate(1)
    await expect(gate.enqueue(() => Promise.resolve(42))).resolves.toBe(42)
    await expect(gate.enqueue(() => Promise.reject(new Error('boom')))).rejects.toThrow('boom')
    await expect(gate.enqueue(() => {
      throw new Error('sync boom')
    })).rejects.toThrow('sync boom')
    // The slot survived all failure shapes.
    await expect(gate.enqueue(() => Promise.resolve('after'))).resolves.toBe('after')
    expect(gate.queuedCount()).toBe(0)
  })
})
