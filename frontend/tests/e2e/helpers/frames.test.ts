import type { Locator, Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { settleFrames, settleTransitions } from './frames'

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

/** A stand-in for the `CSSTransition` of the browser, which neither vitest environment defines. */
class FakeTransition {
  readonly finished: Promise<unknown>

  constructor(finished: Promise<unknown>) {
    this.finished = finished
  }
}

/** An animation that is not a transition, and never finishes, as a spinner does not. */
const endlessAnimation = { finished: new Promise<never>(() => {}) }

/** A promise that the test settles from outside. */
function deferred(): { promise: Promise<void>, resolve: () => void, reject: (reason: unknown) => void } {
  let resolve!: () => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<void>((onResolve, onReject) => {
    resolve = onResolve
    reject = onReject
  })
  return { promise, resolve, reject }
}

/** A scope whose element holds `animations`, with `evaluate` running in this test's realm. */
function transitionScope(animations: readonly unknown[]): { scope: Locator, getAnimations: ReturnType<typeof vi.fn> } {
  const getAnimations = vi.fn(() => animations)
  const element = { getAnimations }
  const scope = {
    evaluate: async (run: (element: unknown) => unknown) => run(element),
    page: () => realmPage(),
  } as unknown as Locator
  return { scope, getAnimations }
}

describe('settleTransitions', () => {
  it('waits until each transition in the subtree ended, then for two frames', async () => {
    vi.stubGlobal('CSSTransition', FakeTransition)
    const frames = stubFrames()
    const first = deferred()
    const second = deferred()
    const { scope, getAnimations } = transitionScope([new FakeTransition(first.promise), new FakeTransition(second.promise)])
    let settled = false
    const done = settleTransitions(scope).then(() => {
      settled = true
    })
    await flushMicrotasks()
    expect(getAnimations).toHaveBeenCalledWith({ subtree: true })

    first.resolve()
    await flushMicrotasks()
    expect(frames.pending(), 'no frame wait starts while a transition runs').toBe(0)

    second.resolve()
    await flushMicrotasks()
    expect(frames.pending()).toBe(1)
    frames.runFrame()
    await flushMicrotasks()
    expect(settled).toBe(false)
    frames.runFrame()
    await done
    expect(settled).toBe(true)
  })

  it('ends the wait for a transition that a later change cancels', async () => {
    vi.stubGlobal('CSSTransition', FakeTransition)
    const frames = stubFrames()
    const cancelled = deferred()
    const { scope } = transitionScope([new FakeTransition(cancelled.promise)])
    const done = settleTransitions(scope)
    cancelled.reject(new DOMException('The transition was cancelled.', 'AbortError'))
    await flushMicrotasks()
    frames.runFrame()
    frames.runFrame()
    await expect(done).resolves.toBeUndefined()
  })

  it('leaves an animation that is not a transition out of the wait', async () => {
    vi.stubGlobal('CSSTransition', FakeTransition)
    const frames = stubFrames()
    const { scope } = transitionScope([endlessAnimation])
    const done = settleTransitions(scope)
    await flushMicrotasks()
    frames.runFrame()
    frames.runFrame()
    await expect(done).resolves.toBeUndefined()
  })

  it('waits only for the frames when the subtree runs no animation', async () => {
    vi.stubGlobal('CSSTransition', FakeTransition)
    const frames = stubFrames()
    const { scope } = transitionScope([])
    const done = settleTransitions(scope)
    await flushMicrotasks()
    expect(frames.pending()).toBe(1)
    frames.runFrame()
    frames.runFrame()
    await expect(done).resolves.toBeUndefined()
  })
})
