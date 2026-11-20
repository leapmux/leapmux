import type { BrowserAuthKind } from '~/api/platformBridge'
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
  startBrowserAuth: vi.fn<(kind: BrowserAuthKind) => Promise<{ ceremonyId: string, authorizeUrl: string }>>(),
  cancelBrowserAuth: vi.fn<(ceremonyId: string) => Promise<void>>(),
  waitForListener: vi.fn<() => Promise<void> | undefined>(),
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
    startBrowserAuth: (kind: BrowserAuthKind) => bridgeMocks.startBrowserAuth(kind),
    cancelBrowserAuth: (ceremonyId: string) => bridgeMocks.cancelBrowserAuth(ceremonyId),
    onEvent: (_event: string, handler: (...args: unknown[]) => void): Promise<() => void> => {
      bridgeMocks.handlers.push(handler)
      const unlisten = vi.fn(() => {
        const index = bridgeMocks.handlers.indexOf(handler)
        if (index >= 0)
          bridgeMocks.handlers.splice(index, 1)
      })
      bridgeMocks.unlistens.push(unlisten)
      const pending = bridgeMocks.waitForListener()
      return pending
        ? pending.then(() => unlisten)
        : Promise.resolve(unlisten)
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
  bridgeMocks.waitForListener.mockReset()
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

  // A stale result of a superseded ceremony may land before this call's
  // start resolves. Adopting it settles the new ceremony: a stale failure
  // cancels the sign-in silently. Promise resolution is first-wins, so the
  // final value proves the stale result settled nothing.
  it('drops a stale failure of a previous ceremony that arrives before the start returns', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    const flowPromise = runBrowserAuth('sign-in')
    // The listener is up; the start command has not answered yet.
    await settle()

    bridgeMocks.emit({ ceremonyId: 'stale', ok: false, failure: 'expired' })

    start.resolve(START)
    const flow = await flowPromise

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await expect(flow.result).resolves.toEqual({ ok: true, failure: '' })
  })

  // A stale ok would run the success path before the sidecar holds the new
  // session, so it must not settle the result either.
  it('drops a stale ok of a previous ceremony that arrives before the start returns', async () => {
    const start = deferred<{ ceremonyId: string, authorizeUrl: string }>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    const flowPromise = runBrowserAuth('sign-in')
    await settle()

    bridgeMocks.emit({ ceremonyId: 'stale', ok: true, failure: '' })

    start.resolve(START)
    const flow = await flowPromise

    let settled = false
    void flow.result.then(() => {
      settled = true
    })
    await settle()
    expect(settled).toBe(false)

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: false, failure: 'wrong code' })
    await expect(flow.result).resolves.toEqual({ ok: false, failure: 'wrong code' })
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
    const flowPromise = runBrowserAuth('sign-in', controller.signal)
    await settle()
    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledOnce()

    controller.abort()
    start.resolve(START)
    const flow = await flowPromise

    expect(bridgeMocks.openExternalUrl).not.toHaveBeenCalled()
    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
    expect(bridgeMocks.handlers).toHaveLength(1)

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await expect(flow.result).resolves.toEqual({ ok: true, failure: '' })
    expect(bridgeMocks.handlers).toHaveLength(0)
  })

  it('settles as cancelled when the cancel command rejects after the start is issued', async () => {
    const start = deferred<typeof START>()
    bridgeMocks.startBrowserAuth.mockReturnValue(start.promise)
    bridgeMocks.cancelBrowserAuth.mockRejectedValue(new Error('ipc down'))
    const controller = new AbortController()
    const flowPromise = runBrowserAuth('sign-in', controller.signal)
    await settle()
    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledOnce()

    controller.abort()
    start.resolve(START)
    const flow = await flowPromise

    await expect(flow.result).resolves.toEqual({ ok: false, failure: '' })
    expect(bridgeMocks.openExternalUrl).not.toHaveBeenCalled()
    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
    expect(bridgeMocks.handlers).toHaveLength(0)
  })

  it('skips the listener and commands for an already aborted signal', async () => {
    await expect(runBrowserAuth('sign-in', AbortSignal.abort())).rejects.toHaveProperty('name', 'AbortError')

    expect(bridgeMocks.unlistens).toHaveLength(0)
    expect(bridgeMocks.startBrowserAuth).not.toHaveBeenCalled()
    expect(bridgeMocks.cancelBrowserAuth).not.toHaveBeenCalled()
    expect(bridgeMocks.openExternalUrl).not.toHaveBeenCalled()
  })

  it('removes a late listener without starting over the live ceremony', async () => {
    const registration = deferred<void>()
    bridgeMocks.waitForListener.mockReturnValueOnce(registration.promise)
    const controller = new AbortController()
    const abandoned = runBrowserAuth('step-up', controller.signal)
      .then(() => 'started', (err: Error) => err.name)
    expect(bridgeMocks.startBrowserAuth).not.toHaveBeenCalled()

    controller.abort()
    const live = await runBrowserAuth('sign-in')
    registration.resolve()
    await settle()

    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledExactlyOnceWith('sign-in')
    expect(bridgeMocks.cancelBrowserAuth).not.toHaveBeenCalled()
    expect(bridgeMocks.openExternalUrl).toHaveBeenCalledExactlyOnceWith(START.authorizeUrl)
    expect(await abandoned).toBe('AbortError')
    expect(bridgeMocks.unlistens[0]).toHaveBeenCalledOnce()
    expect(bridgeMocks.unlistens[1]).not.toHaveBeenCalled()

    bridgeMocks.emit({ ceremonyId: START.ceremonyId, ok: true, failure: '' })

    await expect(live.result).resolves.toEqual({ ok: true, failure: '' })
    expect(bridgeMocks.unlistens[1]).toHaveBeenCalledOnce()
  })

  it('removes the abort hook after a result while the browser is opening', async () => {
    const controller = new AbortController()
    const open = deferred<void>()
    bridgeMocks.openExternalUrl.mockReturnValue(open.promise)

    const pending = runBrowserAuth('sign-in', controller.signal)
    await settle()
    bridgeMocks.emit({ ceremonyId: 'c-1', ok: true, failure: '' })
    await settle()
    controller.abort()
    open.resolve()
    const flow = await pending

    await expect(flow.result).resolves.toEqual({ ok: true, failure: '' })
    expect(bridgeMocks.cancelBrowserAuth).not.toHaveBeenCalled()
    expect(bridgeMocks.unlistens[0]).toHaveBeenCalledOnce()
  })

  it('removes both listeners when opening the browser fails', async () => {
    const controller = new AbortController()
    bridgeMocks.openExternalUrl.mockRejectedValue(new Error('no browser'))

    await expect(runBrowserAuth('sign-in', controller.signal)).rejects.toThrow('no browser')
    controller.abort()

    expect(bridgeMocks.cancelBrowserAuth).not.toHaveBeenCalled()
    expect(bridgeMocks.unlistens[0]).toHaveBeenCalledOnce()
  })

  // The abort listener must be up before the URL opens: an abort during the
  // open would otherwise be lost, and the ceremony would continue with the
  // authorize page open.
  it('cancels when the signal aborts while the URL is opening', async () => {
    const open = deferred<void>()
    bridgeMocks.openExternalUrl.mockReturnValue(open.promise)
    const controller = new AbortController()
    const flowPromise = runBrowserAuth('sign-in', controller.signal)
    await settle()

    controller.abort()
    open.resolve()
    await flowPromise

    expect(bridgeMocks.openExternalUrl).toHaveBeenCalledExactlyOnceWith(START.authorizeUrl)
    expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1')
  })

  it('removes the listener and rejects when the start rejects', async () => {
    bridgeMocks.startBrowserAuth.mockRejectedValue(new Error('sidecar refused'))

    await expect(runBrowserAuth('sign-in')).rejects.toThrow('sidecar refused')

    expect(bridgeMocks.unlistens[0]).toHaveBeenCalled()
    expect(bridgeMocks.handlers).toHaveLength(0)
  })
})
