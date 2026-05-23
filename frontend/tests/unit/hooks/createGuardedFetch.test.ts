import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { deferred, flush } from '../helpers/async'

describe('createGuardedFetch', () => {
  it('flips loading true during the fetch and back to false on success', async () => {
    const d = deferred<string>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const apply = vi.fn()
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => d.promise,
          applySuccess: apply,
        })
        expect(fetcher.loading()).toBe(false)
        const p = fetcher.run()
        await flush()
        expect(fetcher.loading()).toBe(true)
        d.resolve('ok')
        await p
        expect(fetcher.loading()).toBe(false)
        expect(apply).toHaveBeenCalledWith('ok', undefined)
        dispose()
        done()
      })
    })
  })

  it('drops the result of an older run when a newer one starts mid-flight', async () => {
    const slow = deferred<string>()
    const fast = deferred<string>()
    const fetches = [() => slow.promise, () => fast.promise]
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const apply = vi.fn()
        const fetcher = createGuardedFetch<number, string>({
          fetch: i => fetches[i](),
          applySuccess: apply,
        })
        const p1 = fetcher.run(0)
        const p2 = fetcher.run(1)
        // Resolve the fast (second) run first.
        fast.resolve('fast')
        await p2
        // Now resolve the slow (older) run — its applySuccess must not fire.
        slow.resolve('slow')
        await p1
        expect(apply).toHaveBeenCalledTimes(1)
        expect(apply).toHaveBeenCalledWith('fast', 1)
        dispose()
        done()
      })
    })
  })

  it('run(null) cancels any in-flight run and clears loading immediately', async () => {
    const inflight = deferred<string>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const apply = vi.fn()
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => inflight.promise,
          applySuccess: apply,
        })
        const p1 = fetcher.run()
        await flush()
        expect(fetcher.loading()).toBe(true)
        await fetcher.run(null)
        expect(fetcher.loading()).toBe(false)
        inflight.resolve('late')
        await p1
        // The stale resolution must not call applySuccess.
        expect(apply).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('forwards rejection to onError only when the run is still latest', async () => {
    const first = deferred<string>()
    const second = deferred<string>()
    const fetches = [() => first.promise, () => second.promise]
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const onError = vi.fn()
        const fetcher = createGuardedFetch<number, string>({
          fetch: i => fetches[i](),
          applySuccess: vi.fn(),
          onError,
        })
        const p1 = fetcher.run(0)
        const p2 = fetcher.run(1)
        // The first run rejects, but it's already superseded — onError
        // must NOT fire for it.
        first.reject(new Error('first-boom'))
        await p1
        expect(onError).not.toHaveBeenCalled()
        // The second (latest) run rejects — onError must fire.
        second.reject(new Error('second-boom'))
        await p2
        expect(onError).toHaveBeenCalledTimes(1)
        expect(onError.mock.calls[0][1]).toBe(1)
        expect(fetcher.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('applySuccess runs inside a batch with loading=false', async () => {
    // Regression: GitOptions.fetchBranches relies on the loading flag and
    // the branch list being committed in the same render pass. Verify the
    // helper preserves that invariant by observing the signals from
    // inside applySuccess.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        let loadingDuringApply: boolean | null = null
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => Promise.resolve('ok'),
          applySuccess: () => {
            // Inside the success batch, the helper has already set loading
            // to false; the assertion below proves the two writes batch
            // together (no intervening render with loading=true + new data).
            loadingDuringApply = fetcher.loading()
          },
        })
        await fetcher.run()
        expect(loadingDuringApply).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('onError runs inside a batch with loading=false', async () => {
    // Mirrors the applySuccess batch invariant: callers that touch
    // multiple signals from onError (e.g. setError + clear cached data)
    // must observe loading=false atomically with their writes. Without
    // the batch, error renders fire two separate reactive notifications
    // (one per signal, plus the loading clear).
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        let loadingDuringError: boolean | null = null
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => Promise.reject(new Error('boom')),
          applySuccess: vi.fn(),
          onError: () => {
            // Helper has already cleared loading by the time onError
            // runs inside its batch — proves setLoading(false) and
            // onError land in the same reactive transaction.
            loadingDuringError = fetcher.loading()
          },
        })
        await fetcher.run()
        expect(loadingDuringError).toBe(false)
        expect(fetcher.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('run(null) is a no-op when nothing is in flight', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => Promise.resolve('ok'),
          applySuccess: vi.fn(),
        })
        // No prior run. Cancellation should not throw, should leave
        // loading at its initial false.
        await fetcher.run(null)
        expect(fetcher.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('sequential runs each commit their own result', async () => {
    const apply = vi.fn<(data: string, args: number) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<number, string>({
          fetch: i => Promise.resolve(`v${i}`),
          applySuccess: apply,
        })
        await fetcher.run(1)
        await fetcher.run(2)
        await fetcher.run(3)
        expect(apply).toHaveBeenCalledTimes(3)
        expect(apply.mock.calls[0]).toEqual(['v1', 1])
        expect(apply.mock.calls[1]).toEqual(['v2', 2])
        expect(apply.mock.calls[2]).toEqual(['v3', 3])
        expect(fetcher.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('error then successful retry leaves loading=false and commits the retry result', async () => {
    const apply = vi.fn<(data: string, args: number) => void>()
    const onError = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<number, string>({
          fetch: i => i === 0 ? Promise.reject(new Error('boom')) : Promise.resolve(`v${i}`),
          applySuccess: apply,
          onError,
        })
        await fetcher.run(0)
        expect(onError).toHaveBeenCalledTimes(1)
        expect(fetcher.loading()).toBe(false)
        await fetcher.run(1)
        expect(apply).toHaveBeenCalledTimes(1)
        expect(apply.mock.calls[0]).toEqual(['v1', 1])
        expect(fetcher.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('passes a non-aborted AbortSignal to fetch on each run', async () => {
    const signals: AbortSignal[] = []
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<number, string>({
          fetch: (_, signal) => {
            signals.push(signal)
            return Promise.resolve('ok')
          },
          applySuccess: vi.fn(),
        })
        await fetcher.run(1)
        await fetcher.run(2)
        expect(signals).toHaveLength(2)
        // Each run got its own (distinct) signal; both completed without abort.
        expect(signals[0]).not.toBe(signals[1])
        expect(signals[0].aborted).toBe(false)
        expect(signals[1].aborted).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('aborts the prior run\'s signal when a newer run starts', async () => {
    const slow = deferred<string>()
    const fast = deferred<string>()
    const fetches = [() => slow.promise, () => fast.promise]
    const signals: AbortSignal[] = []
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<number, string>({
          fetch: (i, signal) => {
            signals.push(signal)
            return fetches[i]()
          },
          applySuccess: vi.fn(),
        })
        const p1 = fetcher.run(0)
        await flush()
        expect(signals[0].aborted).toBe(false)
        const p2 = fetcher.run(1)
        // The first run's signal must be aborted as soon as the second starts.
        expect(signals[0].aborted).toBe(true)
        expect(signals[1].aborted).toBe(false)
        fast.resolve('fast')
        await p2
        slow.resolve('slow')
        await p1
        dispose()
        done()
      })
    })
  })

  it('aborts every prior signal during a rapid burst of runs (N >= 3)', async () => {
    // The 2-run supersede case is covered above; this guards against a
    // regression where only the immediately-prior controller is aborted
    // instead of all in-flight ones. With three quick `run` calls the
    // first two must both be aborted, only the third stays live.
    const signals: AbortSignal[] = []
    const deferreds = [deferred<string>(), deferred<string>(), deferred<string>()]
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<number, string>({
          fetch: (i, signal) => {
            signals.push(signal)
            return deferreds[i].promise
          },
          applySuccess: vi.fn(),
        })
        const promises = [fetcher.run(0), fetcher.run(1), fetcher.run(2)]
        await flush()
        expect(signals).toHaveLength(3)
        expect(signals[0].aborted).toBe(true)
        expect(signals[1].aborted).toBe(true)
        expect(signals[2].aborted).toBe(false)
        deferreds.forEach(d => d.resolve('x'))
        await Promise.allSettled(promises)
        dispose()
        done()
      })
    })
  })

  it('run(null) aborts the in-flight signal', async () => {
    const d = deferred<string>()
    let capturedSignal: AbortSignal | null = null
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<void, string>({
          fetch: (_, signal) => {
            capturedSignal = signal
            return d.promise
          },
          applySuccess: vi.fn(),
        })
        const p = fetcher.run()
        await flush()
        expect(capturedSignal!.aborted).toBe(false)
        await fetcher.run(null)
        expect(capturedSignal!.aborted).toBe(true)
        // Unblock the now-cancelled run so the test doesn't leak the promise.
        d.resolve('late')
        await p
        dispose()
        done()
      })
    })
  })

  it('abort-driven rejection from a superseded run is silently swallowed (no onError)', async () => {
    // The user's fetch function rejects with the AbortError when its signal
    // fires (this is how `fetch()` and abort-aware RPCs behave). Because
    // the rejecting run is no longer the latest generation, the helper
    // must NOT forward that rejection to onError — only real failures of
    // the current run are user-visible.
    const onError = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<number, string>({
          fetch: (i, signal) => new Promise<string>((resolve, reject) => {
            if (i === 0) {
              signal.addEventListener('abort', () => reject(signal.reason))
              return
            }
            resolve(`v${i}`)
          }),
          applySuccess: vi.fn(),
          onError,
        })
        const p1 = fetcher.run(0)
        await flush()
        const p2 = fetcher.run(1)
        await Promise.allSettled([p1, p2])
        expect(onError).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('owner dispose aborts the inflight signal and clears loading', async () => {
    // A dialog dismissed mid-probe destroys the reactive owner. The
    // helper's onCleanup must abort the inflight controller so the
    // wasted network round-trip stops, and reset loading so any
    // surviving spinner accessor reads false.
    const d = deferred<string>()
    let capturedSignal: AbortSignal | null = null
    let fetcherRef!: ReturnType<typeof createGuardedFetch<void, string>>
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        fetcherRef = createGuardedFetch<void, string>({
          fetch: (_, signal) => {
            capturedSignal = signal
            return d.promise
          },
          applySuccess: vi.fn(),
        })
        const p = fetcherRef.run()
        await flush()
        expect(fetcherRef.loading()).toBe(true)
        expect(capturedSignal!.aborted).toBe(false)
        dispose()
        expect(capturedSignal!.aborted).toBe(true)
        expect(fetcherRef.loading()).toBe(false)
        // Unblock the cancelled run so the test doesn't leak the promise.
        d.resolve('late')
        await p
        done()
      })
    })
  })

  it('owner dispose suppresses applySuccess for a still-resolving inflight run', async () => {
    // After dispose the underlying transport may still resolve before
    // the AbortError propagates. Generation bumping in onCleanup must
    // defang that late resolution so applySuccess doesn't fire into a
    // dead reactive scope.
    const d = deferred<string>()
    const apply = vi.fn()
    let fetcherRef!: ReturnType<typeof createGuardedFetch<void, string>>
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        fetcherRef = createGuardedFetch<void, string>({
          fetch: () => d.promise,
          applySuccess: apply,
        })
        const p = fetcherRef.run()
        await flush()
        dispose()
        d.resolve('late')
        await p
        expect(apply).not.toHaveBeenCalled()
        done()
      })
    })
  })

  it('does NOT route an applySuccess throw to onError (programmer bug, not fetch failure)', async () => {
    // Regression: applySuccess used to run inside the same try/catch as
    // the fetch. A throw from applySuccess (a downstream signal listener
    // throwing, a proto-field access on an unexpected shape) was caught
    // and forwarded to onError, leaving the caller with half-applied
    // success state PLUS a fetch-failure UX banner for a fetch that
    // actually succeeded. Now apply errors are isolated in their own
    // try/catch and logged via console.error — onError stays untouched
    // and the fetcher doesn't crash the surrounding dialog.
    const onError = vi.fn()
    // The logger funnels into console.warn with the source prefix —
    // spy on console.warn to capture it without coupling to the
    // logger's internals.
    const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const fetcher = createGuardedFetch<void, string>({
            fetch: () => Promise.resolve('ok'),
            applySuccess: () => {
              throw new Error('apply-boom')
            },
            onError,
          })
          await fetcher.run()
          expect(onError).not.toHaveBeenCalled()
          // The apply throw is logged so the bug stays visible — pin
          // that the throw isn't silently swallowed.
          expect(consoleWarn).toHaveBeenCalledWith(
            '[createGuardedFetch]',
            'applySuccess threw',
            expect.any(Error),
          )
          // Loading still cleared even though apply threw — the batch
          // setLoading(false) ran before the throw.
          expect(fetcher.loading()).toBe(false)
          dispose()
          done()
        })
      })
    }
    finally {
      consoleWarn.mockRestore()
    }
  })

  it('skipLoadingFlash suppresses the setLoading(true) for that run', async () => {
    const d = deferred<string>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        let skip = false
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => d.promise,
          applySuccess: vi.fn(),
          skipLoadingFlash: () => skip,
        })
        skip = true
        const p = fetcher.run()
        await flush()
        expect(fetcher.loading()).toBe(false)
        d.resolve('ok')
        await p
        expect(fetcher.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('post-dispose run() is a no-op: no fetch fires, no signals written', async () => {
    // Regression: runImpl had no `disposed` bail-out, so a stale closure
    // (e.g. a Promise.then handler captured before dispose) that called
    // run() after cleanup would re-arm inflight and write setLoading on
    // a torn-down reactive scope. The `disposed` gate at the top of
    // runImpl must short-circuit so post-dispose calls produce no
    // side effects at all.
    const fetch = vi.fn().mockResolvedValue('ok')
    const apply = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<void, string>({
          fetch,
          applySuccess: apply,
        })
        dispose()
        // After dispose: run() must be a no-op. Wait one microtask to
        // let any (incorrectly) un-gated code fire.
        await fetcher.run()
        await flush()
        expect(fetch).not.toHaveBeenCalled()
        expect(apply).not.toHaveBeenCalled()
        done()
      })
    })
  })

  it('post-dispose response is dropped: applySuccess does NOT fire on a stale fetch', async () => {
    // Companion to the above: even if a fetch was IN FLIGHT at dispose
    // time, the eventual response must not invoke applySuccess on the
    // dead scope. Generation guarding handles this today; the disposed
    // flag is documentation-as-code for the same property.
    const d = deferred<string>()
    const apply = vi.fn()
    const onError = vi.fn()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const fetcher = createGuardedFetch<void, string>({
          fetch: () => d.promise,
          applySuccess: apply,
          onError,
        })
        const p = fetcher.run()
        await flush()
        dispose()
        d.resolve('late')
        await p
        await flush()
        expect(apply).not.toHaveBeenCalled()
        expect(onError).not.toHaveBeenCalled()
        done()
      })
    })
  })
})
