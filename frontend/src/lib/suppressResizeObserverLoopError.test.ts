import { afterEach, describe, expect, it, vi } from 'vitest'
import { installResizeObserverLoopErrorSuppressor, isResizeObserverLoopError } from './suppressResizeObserverLoopError'

describe('isResizeObserverLoopError', () => {
  it('matches both known ResizeObserver loop messages', () => {
    expect(isResizeObserverLoopError('ResizeObserver loop completed with undelivered notifications')).toBe(true)
    expect(isResizeObserverLoopError('ResizeObserver loop completed with undelivered notifications.')).toBe(true)
    expect(isResizeObserverLoopError('ResizeObserver loop limit exceeded')).toBe(true)
  })

  it('does not match unrelated error messages', () => {
    expect(isResizeObserverLoopError('TypeError: x is not a function')).toBe(false)
    expect(isResizeObserverLoopError('Uncaught ResizeObserver loop completed with undelivered notifications')).toBe(false)
    expect(isResizeObserverLoopError('')).toBe(false)
  })

  it('does not match non-string values', () => {
    expect(isResizeObserverLoopError(undefined)).toBe(false)
    expect(isResizeObserverLoopError(null)).toBe(false)
    expect(isResizeObserverLoopError(42)).toBe(false)
    expect(isResizeObserverLoopError(new Error('ResizeObserver loop limit exceeded'))).toBe(false)
  })
})

describe('installResizeObserverLoopErrorSuppressor', () => {
  const disposers: Array<() => void> = []

  afterEach(() => {
    while (disposers.length)
      disposers.pop()!()
    vi.restoreAllMocks()
  })

  function install(target?: Parameters<typeof installResizeObserverLoopErrorSuppressor>[0]) {
    const dispose = installResizeObserverLoopErrorSuppressor(target)
    disposers.push(dispose)
    return dispose
  }

  function dispatchError(message: string): { defaultPrevented: boolean } {
    // jsdom's programmatic dispatch does not implement ErrorEvent's default
    // action, so preventDefault() cannot flip defaultPrevented here. Assert
    // downstream-listener suppression (stopImmediatePropagation) instead, which
    // is the load-bearing behavior for the dev overlay.
    const event = new ErrorEvent('error', { message, cancelable: true })
    window.dispatchEvent(event)
    return event
  }

  it('stops a later window error listener (the dev overlay) from seeing the RO loop error', () => {
    install()
    // Registered AFTER the suppressor, mirroring the dev overlay whose listener
    // is added during mount(); the suppressor runs first and stops propagation.
    const overlayListener = vi.fn()
    window.addEventListener('error', overlayListener)
    disposers.push(() => window.removeEventListener('error', overlayListener))

    dispatchError('ResizeObserver loop completed with undelivered notifications')

    expect(overlayListener).not.toHaveBeenCalled()
  })

  it('lets unrelated errors reach a later listener', () => {
    install()
    const overlayListener = vi.fn()
    window.addEventListener('error', overlayListener)
    disposers.push(() => window.removeEventListener('error', overlayListener))

    dispatchError('TypeError: boom')

    expect(overlayListener).toHaveBeenCalledTimes(1)
  })

  it('calls preventDefault on the RO loop error and not on others', () => {
    install()
    const roEvent = new ErrorEvent('error', {
      message: 'ResizeObserver loop limit exceeded',
      cancelable: true,
    })
    const roPrevent = vi.spyOn(roEvent, 'preventDefault')
    window.dispatchEvent(roEvent)
    expect(roPrevent).toHaveBeenCalledTimes(1)

    const otherEvent = new ErrorEvent('error', { message: 'nope', cancelable: true })
    const otherPrevent = vi.spyOn(otherEvent, 'preventDefault')
    window.dispatchEvent(otherEvent)
    expect(otherPrevent).not.toHaveBeenCalled()
  })

  it('disposer removes the listener so the overlay sees the error again', () => {
    const dispose = install()
    const overlayListener = vi.fn()
    window.addEventListener('error', overlayListener)
    disposers.push(() => window.removeEventListener('error', overlayListener))

    dispose()
    dispatchError('ResizeObserver loop completed with undelivered notifications')

    expect(overlayListener).toHaveBeenCalledTimes(1)
  })

  it('is a no-op when there is no DOM (SSR)', () => {
    // Stub the global so defaultTarget() resolves to undefined (typeof window ===
    // 'undefined'). Calling with no argument then hits the no-op branch. Passing
    // an explicit `undefined` would NOT test this -- it re-triggers the default
    // parameter, which resolves back to window in jsdom.
    vi.stubGlobal('window', undefined)
    try {
      const dispose = installResizeObserverLoopErrorSuppressor()
      expect(() => dispose()).not.toThrow()
    }
    finally {
      vi.unstubAllGlobals()
    }
  })
})
