import type { ServerResponse } from 'node:http'
import { EventEmitter } from 'node:events'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { pauseBetweenChunks } from './responsePause'

/** A response that only emits `close`, which is all that the pause reads. */
function fakeResponse(): ServerResponse & EventEmitter {
  return new EventEmitter() as ServerResponse & EventEmitter
}

describe('pauseBetweenChunks', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('resolves at once for a delay of zero or less', async () => {
    const response = fakeResponse()
    await pauseBetweenChunks(response, 0)
    await pauseBetweenChunks(response, -5)
    expect(response.listenerCount('close')).toBe(0)
  })

  it('resolves when the delay ends, and removes its listener', async () => {
    const response = fakeResponse()
    let resolved = false
    const pause = pauseBetweenChunks(response, 1_000).then(() => {
      resolved = true
    })
    await vi.advanceTimersByTimeAsync(999)
    expect(resolved).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    await pause
    expect(resolved).toBe(true)
    expect(response.listenerCount('close')).toBe(0)
  })

  it('resolves early when the client goes away, and clears its timer', async () => {
    const response = fakeResponse()
    const pause = pauseBetweenChunks(response, 60_000)
    response.emit('close')
    await pause
    expect(response.listenerCount('close')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })
})
