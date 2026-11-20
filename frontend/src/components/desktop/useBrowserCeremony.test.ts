import type { BrowserAuthFlow } from '~/lib/browserAuth'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred, settle } from '~/test-support/async'
import { useBrowserCeremony } from './useBrowserCeremony'

const runBrowserAuth = vi.hoisted(() => vi.fn())
vi.mock('~/lib/browserAuth', () => ({ runBrowserAuth }))

/** One hand-resolvable ceremony the mocked run hands to `start`. */
function flowFixture() {
  const result = deferred<{ ok: boolean, failure: string }>()
  const cancel = vi.fn(() => Promise.resolve())
  const flow: BrowserAuthFlow = {
    url: 'https://hub.example/authorize',
    result: result.promise,
    cancel,
  }
  return { flow, result, cancel }
}

/**
 * Runs `body` in its own reactive root and disposes it at the end, so the
 * hook's `onCleanup` fires the way an unmount does.
 */
async function inCeremonyRoot(
  kind: 'sign-in' | 'step-up',
  onSuccess: () => void | Promise<void>,
  body: (ceremony: ReturnType<typeof useBrowserCeremony>, dispose: () => void) => Promise<void>,
): Promise<void> {
  await createRoot(async (dispose) => {
    const ceremony = useBrowserCeremony(kind, onSuccess)
    await body(ceremony, dispose)
    dispose()
  })
}

describe('useBrowserCeremony', () => {
  beforeEach(() => {
    runBrowserAuth.mockReset()
  })

  it('starts the ceremony and holds its flow while it waits', async () => {
    const fx = flowFixture()
    runBrowserAuth.mockResolvedValue(fx.flow)

    await inCeremonyRoot('sign-in', vi.fn(), async (ceremony) => {
      const done = ceremony.start()
      await settle()

      expect(runBrowserAuth).toHaveBeenCalledExactlyOnceWith('sign-in', expect.any(AbortSignal))
      expect(ceremony.phase()).toBe('waiting')
      expect(ceremony.flow()).toBe(fx.flow)

      fx.result.resolve({ ok: false, failure: '' })
      await done
    })
  })

  it('calls the success continuation when the result settles ok', async () => {
    const fx = flowFixture()
    runBrowserAuth.mockResolvedValue(fx.flow)
    const onSuccess = vi.fn(() => Promise.resolve())

    await inCeremonyRoot('step-up', onSuccess, async (ceremony) => {
      const done = ceremony.start()
      await settle()
      fx.result.resolve({ ok: true, failure: '' })
      await done

      expect(onSuccess).toHaveBeenCalledOnce()
      expect(runBrowserAuth).toHaveBeenCalledExactlyOnceWith('step-up', expect.any(AbortSignal))
      expect(ceremony.failure()).toBe('')
    })
  })

  it('returns to the first state and shows the reason when the result is not ok', async () => {
    const fx = flowFixture()
    runBrowserAuth.mockResolvedValue(fx.flow)
    const onSuccess = vi.fn()

    await inCeremonyRoot('sign-in', onSuccess, async (ceremony) => {
      const done = ceremony.start()
      await settle()
      fx.result.resolve({ ok: false, failure: 'the code expired' })
      await done

      expect(ceremony.phase()).toBe('idle')
      expect(ceremony.flow()).toBeNull()
      expect(ceremony.failure()).toBe('the code expired')
      expect(onSuccess).not.toHaveBeenCalled()
    })
  })

  // A cancel settles the result with no reason, so the first state returns
  // with nothing to show.
  it('shows no reason when the result carries none', async () => {
    const fx = flowFixture()
    runBrowserAuth.mockResolvedValue(fx.flow)

    await inCeremonyRoot('sign-in', vi.fn(), async (ceremony) => {
      const done = ceremony.start()
      await settle()
      fx.result.resolve({ ok: false, failure: '' })
      await done

      expect(ceremony.phase()).toBe('idle')
      expect(ceremony.failure()).toBe('')
    })
  })

  it('shows the thrown error and returns to the first state when the run fails', async () => {
    runBrowserAuth.mockRejectedValue(new Error('the sidecar is not reachable'))
    const onSuccess = vi.fn()

    await inCeremonyRoot('sign-in', onSuccess, async (ceremony) => {
      await ceremony.start()

      expect(ceremony.phase()).toBe('idle')
      expect(ceremony.flow()).toBeNull()
      expect(ceremony.failure()).toBe('the sidecar is not reachable')
      expect(onSuccess).not.toHaveBeenCalled()
    })
  })

  it('shows the thrown error when the success continuation fails', async () => {
    const fx = flowFixture()
    runBrowserAuth.mockResolvedValue(fx.flow)

    await inCeremonyRoot('sign-in', () => Promise.reject(new Error('the account read failed')), async (ceremony) => {
      const done = ceremony.start()
      await settle()
      fx.result.resolve({ ok: true, failure: '' })
      await done

      expect(ceremony.phase()).toBe('idle')
      expect(ceremony.failure()).toBe('the account read failed')
    })
  })

  // The cancel command settles nothing itself: the one result event of the
  // ceremony resets the state.
  it('cancels the running ceremony and leaves the phase to the result event', async () => {
    const fx = flowFixture()
    runBrowserAuth.mockResolvedValue(fx.flow)

    await inCeremonyRoot('sign-in', vi.fn(), async (ceremony) => {
      const done = ceremony.start()
      await settle()

      await ceremony.cancel()

      expect(fx.cancel).toHaveBeenCalledOnce()
      expect(ceremony.phase()).toBe('waiting')

      fx.result.resolve({ ok: false, failure: '' })
      await done
    })
  })

  it('does nothing when it cancels with no ceremony running', async () => {
    await inCeremonyRoot('sign-in', vi.fn(), async (ceremony) => {
      await ceremony.cancel()

      expect(ceremony.phase()).toBe('idle')
    })
  })

  it('aborts the run when the owner disposes', async () => {
    const fx = flowFixture()
    let signal: AbortSignal | undefined
    runBrowserAuth.mockImplementation((_kind: string, runSignal?: AbortSignal) => {
      signal = runSignal
      return Promise.resolve(fx.flow)
    })

    await inCeremonyRoot('sign-in', vi.fn(), async (ceremony, dispose) => {
      const done = ceremony.start()
      await settle()

      dispose()

      expect(signal?.aborted).toBe(true)

      fx.result.resolve({ ok: false, failure: '' })
      await done
    })
  })

  it('clears a shown reason when the next run starts', async () => {
    const firstRun = flowFixture()
    const secondRun = flowFixture()
    runBrowserAuth.mockResolvedValueOnce(firstRun.flow).mockResolvedValueOnce(secondRun.flow)

    await inCeremonyRoot('sign-in', vi.fn(), async (ceremony) => {
      const first = ceremony.start()
      await settle()
      firstRun.result.resolve({ ok: false, failure: 'the code expired' })
      await first
      expect(ceremony.failure()).toBe('the code expired')

      const second = ceremony.start()
      await settle()

      expect(ceremony.failure()).toBe('')

      secondRun.result.resolve({ ok: false, failure: '' })
      await second
    })
  })
})
