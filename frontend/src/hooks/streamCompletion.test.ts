import type { StreamCompletionHandle } from './streamCompletion'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { waitForStreamCompletion } from './streamCompletion'

// fakeHandle exposes manual triggers for end/error so tests can drive
// each completion path without touching the real channel layer.
function fakeHandle(): {
  handle: StreamCompletionHandle
  fireEnd: () => void
  fireError: (err: Error) => void
} {
  let endCb: (() => void) | null = null
  let errorCb: ((err: Error) => void) | null = null
  return {
    handle: {
      onEnd(cb) {
        endCb = cb
      },
      onError(cb) {
        errorCb = cb
      },
    },
    fireEnd() {
      endCb?.()
    },
    fireError(err) {
      errorCb?.(err)
    },
  }
}

describe('waitForStreamCompletion', () => {
  let fake: ReturnType<typeof fakeHandle>
  let ctrl: AbortController

  beforeEach(() => {
    fake = fakeHandle()
    ctrl = new AbortController()
  })

  it('resolves when the stream ends after wait starts', async () => {
    const promise = waitForStreamCompletion(fake.handle, ctrl.signal)
    fake.fireEnd()
    await expect(promise).resolves.toBeUndefined()
  })

  it('rejects when the stream errors after wait starts', async () => {
    const promise = waitForStreamCompletion(fake.handle, ctrl.signal)
    const err = new Error('transport gone')
    fake.fireError(err)
    await expect(promise).rejects.toBe(err)
  })

  it('resolves when the signal aborts after wait starts', async () => {
    const promise = waitForStreamCompletion(fake.handle, ctrl.signal)
    ctrl.abort()
    await expect(promise).resolves.toBeUndefined()
  })

  // The synchronous-window race: end/error/abort can all fire during the
  // synchronous setup window before the Promise body assigns
  // resolveStream/rejectStream. The flag dance must let those signals
  // be observed instead of leaving the consumer hung.

  it('resolves immediately when end fires during synchronous setup', async () => {
    // A handle that fires onEnd as soon as it's wired — simulates
    // bufferStreamHandle flushing a buffered end on registration.
    const handle: StreamCompletionHandle = {
      onEnd(cb) {
        cb()
      },
      onError() {},
    }
    await expect(waitForStreamCompletion(handle, ctrl.signal)).resolves.toBeUndefined()
  })

  it('rejects immediately when error fires during synchronous setup', async () => {
    const err = new Error('flushed pre-registration error')
    const handle: StreamCompletionHandle = {
      onEnd() {},
      onError(cb) {
        cb(err)
      },
    }
    await expect(waitForStreamCompletion(handle, ctrl.signal)).rejects.toBe(err)
  })

  // Load-bearing regression test: caught a real bug where
  // `addEventListener('abort', ..., { once: true })` does NOT fire on an
  // already-aborted signal in standard implementations, so without the
  // entry-time `if (signal.aborted)` guard the helper hangs forever.
  it('resolves when the signal is already aborted at entry', async () => {
    ctrl.abort()
    await expect(waitForStreamCompletion(fake.handle, ctrl.signal)).resolves.toBeUndefined()
  })

  it('removes the abort listener once end fires before abort', async () => {
    const removeSpy = vi.spyOn(ctrl.signal, 'removeEventListener')
    const promise = waitForStreamCompletion(fake.handle, ctrl.signal)
    fake.fireEnd()
    await promise
    expect(removeSpy).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it('removes the abort listener once error fires before abort', async () => {
    const removeSpy = vi.spyOn(ctrl.signal, 'removeEventListener')
    const promise = waitForStreamCompletion(fake.handle, ctrl.signal)
    fake.fireError(new Error('boom'))
    await expect(promise).rejects.toBeInstanceOf(Error)
    expect(removeSpy).toHaveBeenCalledWith('abort', expect.any(Function))
  })
})
