import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { deferred, flush } from '../helpers/async'

// createLoadingSignal debounces stop() by 1s so a short submit doesn't
// flash the spinner. Use fake timers across all tests so we can advance
// past the debounce deterministically when asserting loading drops.
beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useDialogSubmit', () => {
  it('happy path: run flips loading true, clears error, settles loading false after debounce', async () => {
    const d = deferred<void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { submitting, error, run } = useDialogSubmit()
        expect(submitting.loading()).toBe(false)
        expect(error()).toBeNull()
        const p = run(() => d.promise)
        await flush()
        expect(submitting.loading()).toBe(true)
        d.resolve()
        await p
        // stop() was called but the 1s debounce keeps loading true.
        expect(submitting.loading()).toBe(true)
        await vi.advanceTimersByTimeAsync(1100)
        expect(submitting.loading()).toBe(false)
        expect(error()).toBeNull()
        dispose()
        done()
      })
    })
  })

  it('error path: captures err.message via default formatError', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, run } = useDialogSubmit()
        await run(() => Promise.reject(new Error('boom')))
        expect(error()).toBe('boom')
        dispose()
        done()
      })
    })
  })

  it('default formatError falls back to "Operation failed" for non-Error rejections', async () => {
    // Throw a bare string — the default formatError only unwraps
    // Error.message and must not crash on other shapes.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, run } = useDialogSubmit()
        // eslint-disable-next-line prefer-promise-reject-errors -- intentional non-Error rejection to exercise the formatError fallback
        await run(() => Promise.reject('plain string'))
        expect(error()).toBe('Operation failed')
        dispose()
        done()
      })
    })
  })

  it('fallback overrides the default non-Error message but keeps Error.message extraction', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, run } = useDialogSubmit({ fallback: 'Delete failed' })
        // Error path: fallback is unused — err.message wins.
        await run(() => Promise.reject(new Error('inner')))
        expect(error()).toBe('inner')
        // Non-Error path: fallback replaces the default 'Operation failed'.
        // eslint-disable-next-line prefer-promise-reject-errors -- intentional non-Error rejection to exercise the fallback
        await run(() => Promise.reject('plain string'))
        expect(error()).toBe('Delete failed')
        dispose()
        done()
      })
    })
  })

  it('custom formatError overrides both the Error and non-Error fallback', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, run } = useDialogSubmit({
          formatError: err => err instanceof Error ? `delete: ${err.message}` : 'Delete failed',
        })
        await run(() => Promise.reject(new Error('inner')))
        expect(error()).toBe('delete: inner')
        // eslint-disable-next-line prefer-promise-reject-errors -- intentional non-Error rejection to exercise the formatError fallback
        await run(() => Promise.reject(42))
        expect(error()).toBe('Delete failed')
        dispose()
        done()
      })
    })
  })

  it('clears the previous error before running the next submit', async () => {
    // A retry-after-failure pattern: the prior failure's message must
    // not still be visible while the retry is in flight.
    const d = deferred<void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, run } = useDialogSubmit()
        await run(() => Promise.reject(new Error('first')))
        expect(error()).toBe('first')
        const p = run(() => d.promise)
        await flush()
        expect(error()).toBeNull()
        d.resolve()
        await p
        expect(error()).toBeNull()
        dispose()
        done()
      })
    })
  })

  it('loading drops to false after debounce even when fn rejects', async () => {
    // Regression guard: a thrown rejection must still call stop() in
    // finally, otherwise the disabled-while-busy footer button would
    // stay disabled forever after a single failed retry.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { submitting, run } = useDialogSubmit()
        await run(() => Promise.reject(new Error('boom')))
        await vi.advanceTimersByTimeAsync(1100)
        expect(submitting.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('setError is exposed so callers can sink errors from non-submit paths', async () => {
    // DeleteBranchDialog's refreshInspect runs outside `run()` (it's
    // not the submit body) but writes into the same error signal so
    // the dialog renders one consistent error region.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, setError } = useDialogSubmit()
        setError('inspect failed')
        expect(error()).toBe('inspect failed')
        setError(null)
        expect(error()).toBeNull()
        dispose()
        done()
      })
    })
  })

  it('synchronous throw inside fn is captured and settles loading=false after debounce', async () => {
    // An async arrow can still throw synchronously before the first
    // await. The Promise wrapping makes the try/catch catch it; verify
    // the helper handles that path the same as an awaited rejection.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { submitting, error, run } = useDialogSubmit()
        await run(async () => {
          throw new Error('sync')
        })
        expect(error()).toBe('sync')
        await vi.advanceTimersByTimeAsync(1100)
        expect(submitting.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('onError receives the raw err and suppresses the default setError write', async () => {
    // For callers that surface errors via toast (PushBranchButton),
    // the formatted-string sink is the wrong shape — they need the
    // raw err to pass into showWarnToast. onError replaces the default
    // setError path entirely so there are not two writers to the same
    // signal.
    const failure = new Error('remote unreachable')
    const onError = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, run } = useDialogSubmit({ onError })
        await run(() => Promise.reject(failure))
        expect(onError).toHaveBeenCalledTimes(1)
        expect(onError).toHaveBeenCalledWith(failure)
        // Default setError must NOT have fired — the toast caller owns
        // the error UX end-to-end.
        expect(error()).toBeNull()
        dispose()
        done()
      })
    })
  })

  it('onError still fires when fn throws a non-Error literal', async () => {
    // Guard against future regressions where the onError path was
    // accidentally restricted to Error instances. The raw err passes
    // through regardless of shape.
    const onError = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { run } = useDialogSubmit({ onError })
        // eslint-disable-next-line prefer-promise-reject-errors -- intentional non-Error rejection to exercise the onError shape
        await run(() => Promise.reject('plain'))
        expect(onError).toHaveBeenCalledWith('plain')
        dispose()
        done()
      })
    })
  })

  it('onError + loading still settles to false after debounce', async () => {
    // Regression guard: the onError branch must not skip the
    // submitting.stop() in finally — otherwise a failed push would
    // leave the button disabled forever.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { submitting, run } = useDialogSubmit({ onError: () => {} })
        await run(() => Promise.reject(new Error('boom')))
        await vi.advanceTimersByTimeAsync(1100)
        expect(submitting.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('onError does not fire on a successful submit', async () => {
    const onError = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { run } = useDialogSubmit({ onError })
        await run(() => Promise.resolve())
        expect(onError).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('formHandler: prevents default, runs body, clears error', async () => {
    const body = vi.fn(() => Promise.resolve())
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, formHandler } = useDialogSubmit()
        const handler = formHandler(() => false, body)
        // Seed error first so the "clears error" leg of this test isn't
        // a tautology — a body that never sets error would still leave
        // error()===null without proving the run() wrapper cleared it.
        await formHandler(() => false, () => Promise.reject(new Error('seed')))(
          new Event('submit', { cancelable: true }),
        )
        expect(error()).toBe('seed')

        const event = new Event('submit', { cancelable: true })
        const spy = vi.spyOn(event, 'preventDefault')
        await handler(event)
        expect(spy).toHaveBeenCalledTimes(1)
        expect(body).toHaveBeenCalledTimes(1)
        expect(error()).toBeNull()
        dispose()
        done()
      })
    })
  })

  it('formHandler: re-evaluates disabled() per invocation', async () => {
    // The disabled accessor is called each time the handler fires, not
    // snapshotted at construction. Without this, toggling the underlying
    // submit-disabled state in a form lifecycle (e.g. typed input
    // clears an error) wouldn't unblock the second submit.
    let allowed = false
    const body = vi.fn(() => Promise.resolve())
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { formHandler } = useDialogSubmit()
        const handler = formHandler(() => !allowed, body)
        await handler(new Event('submit', { cancelable: true }))
        expect(body).not.toHaveBeenCalled()
        allowed = true
        await handler(new Event('submit', { cancelable: true }))
        expect(body).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('formHandler: submitting flips true while the body is in flight', async () => {
    const d = deferred<void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { submitting, formHandler } = useDialogSubmit()
        const handler = formHandler(() => false, () => d.promise)
        expect(submitting.loading()).toBe(false)
        const p = handler(new Event('submit', { cancelable: true }))
        await flush()
        expect(submitting.loading()).toBe(true)
        d.resolve()
        await p
        // The createLoadingSignal debounce keeps loading() true briefly
        // after stop() — advance past it to confirm the flag actually
        // settles, instead of asserting against a still-debouncing value.
        await vi.advanceTimersByTimeAsync(1100)
        expect(submitting.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('formHandler: synchronously-thrown body errors land in the error sink', async () => {
    // run() wraps the body in a try/catch — a body that throws BEFORE
    // returning a promise (e.g. a `throw new Error(...)` at the top of
    // an `async` arrow) must route through the same sink as a rejected
    // promise. Otherwise the dialog would silently swallow the throw.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, formHandler } = useDialogSubmit()
        const handler = formHandler(() => false, () => {
          throw new Error('sync boom')
        })
        await handler(new Event('submit', { cancelable: true }))
        expect(error()).toBe('sync boom')
        dispose()
        done()
      })
    })
  })

  it('formHandler: bails when disabled() returns true', async () => {
    const body = vi.fn(() => Promise.resolve())
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { formHandler } = useDialogSubmit()
        const handler = formHandler(() => true, body)
        const event = new Event('submit', { cancelable: true })
        const spy = vi.spyOn(event, 'preventDefault')
        await handler(event)
        // preventDefault still runs — the browser must not submit the
        // form even when the dialog is in a disabled state.
        expect(spy).toHaveBeenCalledTimes(1)
        expect(body).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('formHandler: routes body rejection through the standard error sink', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { error, formHandler } = useDialogSubmit()
        const handler = formHandler(() => false, () => Promise.reject(new Error('boom')))
        await handler(new Event('submit', { cancelable: true }))
        expect(error()).toBe('boom')
        dispose()
        done()
      })
    })
  })

  it('reentrancy guard: a second run() call during an in-flight submit is a no-op', async () => {
    // Programmatic callers (tests, hotkey bindings, parents composing
    // their own submit flow) bypass formHandler.disabled(), so the
    // run() body itself must reject overlapping submits. Without the
    // guard, two parallel CheckoutBranch RPCs (or worse — two parallel
    // DeleteBranch) would fire and the worker would happily execute
    // both, leaving HEAD on switchTo with a confusing "branch already
    // gone" follow-up error.
    const d = deferred<void>()
    const body = vi.fn(() => d.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { run } = useDialogSubmit()
        const p1 = run(body)
        await flush()
        expect(body).toHaveBeenCalledTimes(1)
        // Second concurrent call must NOT invoke the body again.
        const p2 = run(body)
        await flush()
        expect(body).toHaveBeenCalledTimes(1)
        // Resolve the first call so both promises settle and the guard
        // clears. Subsequent calls after settle must work normally.
        d.resolve()
        await p1
        await p2
        const d2 = deferred<void>()
        const second = vi.fn(() => d2.promise)
        const p3 = run(second)
        await flush()
        expect(second).toHaveBeenCalledTimes(1)
        d2.resolve()
        await p3
        dispose()
        done()
      })
    })
  })
})
