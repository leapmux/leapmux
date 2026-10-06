import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { settleFrames } from './frames'

/** A page whose `evaluate` runs the function in this test's own realm, as the browser runs it in the page. */
function realmPage(): Pick<Page, 'evaluate'> {
  return {
    evaluate: (async (run: (arg: unknown) => unknown, arg: unknown) => run(arg)) as Page['evaluate'],
  }
}

/**
 * A `requestAnimationFrame` that this test drives. `runFrame` runs the callbacks that the frame holds, as one
 * rendering step does. A callback that a callback requests waits for the next frame.
 */
function stubFrames(): { pending: () => number, runFrame: () => void } {
  let queued: FrameRequestCallback[] = []
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    queued.push(callback)
    return queued.length
  })
  return {
    pending: () => queued.length,
    runFrame: () => {
      const frame = queued
      queued = []
      for (const callback of frame)
        callback(0)
    },
  }
}

/** Let each settled promise run its reactions. */
async function flushMicrotasks(): Promise<void> {
  for (let turn = 0; turn < 5; turn++)
    await Promise.resolve()
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('settleFrames', () => {
  it('resolves in the second frame after the call, not in the first', async () => {
    const frames = stubFrames()
    let settled = false
    const done = settleFrames(realmPage()).then(() => {
      settled = true
    })
    await flushMicrotasks()
    expect(frames.pending()).toBe(1)

    frames.runFrame()
    await flushMicrotasks()
    expect(settled).toBe(false)
    expect(frames.pending()).toBe(1)

    frames.runFrame()
    await done
    expect(settled).toBe(true)
  })
})
