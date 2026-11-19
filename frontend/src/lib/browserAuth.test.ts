import { beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred, settle } from '~/test-support/async'
import { runBrowserAuth } from './browserAuth'

/**
 * The ceremony seams: the sidecar's two commands and its one result event per
 * ceremony. `onEvent` hands the callback the emitted PAYLOAD -- the fields the
 * shell forwards for `BrowserAuthResultEvent` -- never the Tauri event
 * envelope, so `emit` below delivers exactly what the shell's `emit` carries.
 */
const bridgeMocks = vi.hoisted(() => ({
  openExternalUrl: vi.fn<(url: string) => Promise<void>>(),
  startBrowserAuth: vi.fn<() => Promise<{ ceremonyId: string, authorizeUrl: string }>>(),
  cancelBrowserAuth: vi.fn<(ceremonyId: string) => Promise<void>>(),
  handlers: [] as ((...args: unknown[]) => void)[],
  unlistens: [] as ReturnType<typeof vi.fn>[],
  emit(payload: { ceremonyId: string, ok: boolean, failure: string }): void {
    for (const handler of [...bridgeMocks.handlers])
      handler(payload)
  },
}))

vi.mock('~/api/platformBridge', () => ({
  openExternalUrl: (url: string) => bridgeMocks.openExternalUrl(url),
  platformBridge: {
    startBrowserAuth: () => bridgeMocks.startBrowserAuth(),
    cancelBrowserAuth: (ceremonyId: string) => bridgeMocks.cancelBrowserAuth(ceremonyId),
    onEvent: (_event: string, handler: (...args: unknown[]) => void): Promise<() => void> => {
      bridgeMocks.handlers.push(handler)
      const unlisten = vi.fn(() => {
        const index = bridgeMocks.handlers.indexOf(handler)
        if (index >= 0)
          bridgeMocks.handlers.splice(index, 1)
      })
      bridgeMocks.unlistens.push(unlisten)
      return Promise.resolve(unlisten)
    },
  },
}))

const START = {
  ceremonyId: 'c-1',
  authorizeUrl: 'https://hub.example/authorize',
}

beforeEach(() => {
  bridgeMocks.openExternalUrl.mockReset()
  bridgeMocks.openExternalUrl.mockResolvedValue(undefined)
  bridgeMocks.startBrowserAuth.mockReset()
  bridgeMocks.startBrowserAuth.mockResolvedValue(START)
  bridgeMocks.cancelBrowserAuth.mockReset()
  bridgeMocks.cancelBrowserAuth.mockResolvedValue(undefined)
  bridgeMocks.handlers.length = 0
  bridgeMocks.unlistens.length = 0
})

describe('runBrowserAuth', () => {
  it('resolves with the ok and failure of its own result', async () => {
    const flow = await runBrowserAuth('sign-in')

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await expect(flow.result).resolves.toEqual({ ok: true, failure: '' })
  })

  // A second ceremony may share the event name; only the id this call started
  // may settle its result. Promise resolution is first-wins, so the final
  // value proves the foreign result settled nothing.
  it('ignores a result of another ceremony and settles only with its own', async () => {
    const flow = await runBrowserAuth('sign-in')

    bridgeMocks.emit({ ceremonyId: 'other', ok: true, failure: '' })
    bridgeMocks.emit({ ceremonyId: 'c-1', ok: false, failure: 'wrong code' })

    await expect(flow.result).resolves.toEqual({ ok: false, failure: 'wrong code' })
  })

  it('resolves with a result that arrives before the start returns', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    const flowPromise = runBrowserAuth('sign-in')
    // The listener is up; the start command has not answered yet.
    await settle()

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    start.resolve(START)
    const flow = await flowPromise

    await expect(flow.result).resolves.toEqual({ ok: true, failure: '' })
  })

  // `cancel` only asks the sidecar to cancel; the sidecar's one event decides
  // the outcome, so a cancel that raced a Hub-accepted code still settles ok.
  it('settles only when the event arrives after cancel, then removes the listener', async () => {
    const flow = await runBrowserAuth('sign-in')

    await flow.cancel()

    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
    let settled = false
    void flow.result.then(() => {
      settled = true
    })
    await settle()
    expect(settled).toBe(false)

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: false, failure: '' })

    await expect(flow.result).resolves.toEqual({ ok: false, failure: '' })
    await settle()
    expect(bridgeMocks.unlistens[0]).toHaveBeenCalled()
    expect(bridgeMocks.handlers).toHaveLength(0)
  })

  it('settles with ok true when the event races a cancel', async () => {
    const flow = await runBrowserAuth('sign-in')

    await flow.cancel()
    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await expect(flow.result).resolves.toEqual({ ok: true, failure: '' })
  })

  // The event for that id may never arrive then, so the rejected command is
  // what settles: a cancellation, with no reason to show.
  it('settles with an empty failure when the cancel command rejects', async () => {
    bridgeMocks.cancelBrowserAuth.mockRejectedValue(new Error('ipc down'))
    const flow = await runBrowserAuth('sign-in')

    await flow.cancel()

    await expect(flow.result).resolves.toEqual({ ok: false, failure: '' })
  })

  it('removes the listener once the result settles', async () => {
    const flow = await runBrowserAuth('sign-in')

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await flow.result
    await settle()

    expect(bridgeMocks.unlistens[0]).toHaveBeenCalled()
    expect(bridgeMocks.handlers).toHaveLength(0)
  })

  it('opens no URL and cancels when the signal is aborted before the start resolves', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    const controller = new AbortController()
    controller.abort()
    const flowPromise = runBrowserAuth('sign-in', controller.signal)
    await settle()

    start.resolve(START)
    await flowPromise

    expect(bridgeMocks.openExternalUrl).not.toHaveBeenCalled()
    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
  })

  it('removes the listener and rejects when the start rejects', async () => {
    bridgeMocks.startBrowserAuth.mockRejectedValue(new Error('sidecar refused'))

    await expect(runBrowserAuth('sign-in')).rejects.toThrow('sidecar refused')

    expect(bridgeMocks.unlistens[0]).toHaveBeenCalled()
    expect(bridgeMocks.handlers).toHaveLength(0)
  })
})
