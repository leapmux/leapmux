/// <reference types="vitest/globals" />
import type { GlobalErrorTarget } from './installGlobalErrorSink'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { installGlobalErrorSink } from './installGlobalErrorSink'
import { installResizeObserverLoopErrorSuppressor } from './suppressResizeObserverLoopError'

/**
 * A stand-in for `window` that records its listeners, so a test can fire an
 * event without jsdom's global error plumbing (which reports an uncaught
 * `ErrorEvent` to the console and would make a passing run look broken).
 */
function fakeTarget() {
  const listeners = new Map<string, Set<(event: Event) => void>>()
  const target: GlobalErrorTarget = {
    addEventListener: (type, listener) => {
      const set = listeners.get(type) ?? new Set()
      set.add(listener)
      listeners.set(type, set)
    },
    removeEventListener: (type, listener) => {
      listeners.get(type)?.delete(listener)
    },
  }
  return {
    target,
    listenerCount: (type: string) => listeners.get(type)?.size ?? 0,
    fire: (type: string, event: Partial<Event> & Record<string, unknown>) => {
      for (const listener of listeners.get(type) ?? [])
        listener(event as unknown as Event)
    },
  }
}

describe('installGlobalErrorSink', () => {
  const disposers: Array<() => void> = []

  afterEach(() => {
    while (disposers.length)
      disposers.pop()!()
    vi.restoreAllMocks()
  })

  function install(report: (message: string, err: unknown) => void) {
    const t = fakeTarget()
    disposers.push(installGlobalErrorSink({ report, target: t.target }))
    return t
  }

  it('reports an uncaught error, passing the thrown value through', () => {
    const report = vi.fn()
    const t = install(report)
    const error = new Error('handler blew up')

    t.fire('error', { error, message: 'handler blew up' })

    expect(report).toHaveBeenCalledTimes(1)
    expect(report.mock.calls[0][1]).toBe(error)
  })

  // The whole reason this exists: a rejected promise touches no part of the
  // render graph, so neither ErrorBoundary ever sees it.
  it('reports an unhandled rejection', () => {
    const report = vi.fn()
    const t = install(report)
    const reason = new Error('fetch rejected')

    t.fire('unhandledrejection', { reason })

    expect(report).toHaveBeenCalledTimes(1)
    expect(report.mock.calls[0][1]).toBe(reason)
  })

  it('reports a rejection carrying a non-Error value', () => {
    const report = vi.fn()
    const t = install(report)

    t.fire('unhandledrejection', { reason: 'just a string' })

    expect(report).toHaveBeenCalledTimes(1)
    expect(report.mock.calls[0][1]).toBe('just a string')
  })

  // Chromium fires this once per frame while a long transcript settles. It is
  // self-healing, and a toast per frame would bury the app.
  it('ignores the benign ResizeObserver loop error', () => {
    const report = vi.fn()
    const t = install(report)

    t.fire('error', { message: 'ResizeObserver loop limit exceeded' })
    t.fire('error', { message: 'ResizeObserver loop completed with undelivered notifications' })

    expect(report).not.toHaveBeenCalled()
  })

  it('deduplicates a repeating fault', () => {
    const report = vi.fn()
    const t = install(report)

    for (let i = 0; i < 5; i++)
      t.fire('error', { error: new Error('same every time') })

    expect(report).toHaveBeenCalledTimes(1)
  })

  it('still reports a different fault while one is deduplicated', () => {
    const report = vi.fn()
    const t = install(report)

    t.fire('error', { error: new Error('first') })
    t.fire('error', { error: new Error('first') })
    t.fire('error', { error: new Error('second') })

    expect(report).toHaveBeenCalledTimes(2)
    expect(report.mock.calls[1][1]).toHaveProperty('message', 'second')
  })

  // A message carrying a request id or timestamp differs every time, so the
  // dedupe map must not grow with it.
  it('bounds the messages it remembers', () => {
    const report = vi.fn()
    const t = install(report)

    // Well past the cap, then re-fire the very first message: it must have been
    // evicted, so it reports again rather than being treated as a duplicate.
    for (let i = 0; i < 100; i++)
      t.fire('error', { error: new Error(`unique ${i}`) })
    report.mockClear()

    t.fire('error', { error: new Error('unique 0') })

    expect(report).toHaveBeenCalledTimes(1)
  })

  it('stops reporting once disposed', () => {
    const report = vi.fn()
    const t = install(report)
    disposers.pop()!()

    t.fire('error', { error: new Error('after disposal') })

    expect(report).not.toHaveBeenCalled()
    expect(t.listenerCount('error')).toBe(0)
    expect(t.listenerCount('unhandledrejection')).toBe(0)
  })

  it('is a no-op outside a DOM', () => {
    expect(() => installGlobalErrorSink({ report: vi.fn(), target: undefined })()).not.toThrow()
  })

  // In dev the suppressor is registered first and in the capture phase, so it
  // stops the RO event before this sink's bubble-phase listener runs. Pinned
  // because the two installers' ORDER in `entry-client.tsx` is load-bearing.
  it('never sees an RO error the dev-mode suppressor already stopped', () => {
    const report = vi.fn()
    disposers.push(installResizeObserverLoopErrorSuppressor(window))
    disposers.push(installGlobalErrorSink({ report, target: window }))

    window.dispatchEvent(new ErrorEvent('error', {
      message: 'ResizeObserver loop limit exceeded',
      cancelable: true,
    }))

    expect(report).not.toHaveBeenCalled()
  })
})
