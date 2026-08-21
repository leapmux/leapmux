import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ignorableErrorReason,
  installIgnorableErrorSuppressor,
  isMutedErrorEvent,
  isResizeObserverLoopError,
} from './ignorableErrorEvents'

/**
 * The field shape a browser gives a muted error, as a plain object: the message
 * is sanitized and every locator is stripped. `overrides` puts one field back,
 * which is how the "not muted after all" cases below are written.
 */
function mutedEventFields(overrides: Record<string, unknown> = {}): Event {
  return {
    message: 'Script error.',
    filename: '',
    lineno: 0,
    colno: 0,
    error: null,
    ...overrides,
  } as unknown as Event
}

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

describe('isMutedErrorEvent', () => {
  it('matches the sanitized shape a browser substitutes for a muted error', () => {
    expect(isMutedErrorEvent(mutedEventFields())).toBe(true)
  })

  it('matches the message without its trailing period', () => {
    expect(isMutedErrorEvent(mutedEventFields({ message: 'Script error' }))).toBe(true)
  })

  it('matches a real muted ErrorEvent, whose locators the constructor defaults to empty', () => {
    expect(isMutedErrorEvent(new ErrorEvent('error', { message: 'Script error.' }))).toBe(true)
  })

  it('matches an event double that omits the locator fields entirely', () => {
    // The sink's fakeTarget fires plain partial objects, so absent must read the
    // same as zero/empty here or the sink would toast what this hides.
    expect(isMutedErrorEvent({ message: 'Script error.' } as unknown as Event)).toBe(true)
  })

  // Each of the three below still gives a developer something to work with, so
  // none is ignorable -- this is what keeps the predicate from eating a real fault.
  it('does not match when the event still carries the thrown error', () => {
    expect(isMutedErrorEvent(mutedEventFields({ error: new Error('Script error.') }))).toBe(false)
  })

  it('does not match when the engine supplied a filename', () => {
    expect(isMutedErrorEvent(mutedEventFields({ filename: 'https://cdn.example/app.js' }))).toBe(false)
  })

  it('does not match when the engine supplied a position', () => {
    expect(isMutedErrorEvent(mutedEventFields({ lineno: 42 }))).toBe(false)
    expect(isMutedErrorEvent(mutedEventFields({ colno: 7 }))).toBe(false)
  })

  it('does not match an ordinary error message', () => {
    expect(isMutedErrorEvent(mutedEventFields({ message: 'TypeError: boom' }))).toBe(false)
    expect(isMutedErrorEvent(mutedEventFields({ message: 'Script error. Please reload' }))).toBe(false)
  })

  it('does not match an event with no message at all (a rejection)', () => {
    expect(isMutedErrorEvent({ reason: new Error('rejected') } as unknown as Event)).toBe(false)
  })
})

describe('ignorableErrorReason', () => {
  it('labels each ignorable class', () => {
    expect(ignorableErrorReason(new ErrorEvent('error', {
      message: 'ResizeObserver loop limit exceeded',
    }))).toBe('resize-observer-loop')
    expect(ignorableErrorReason(mutedEventFields())).toBe('muted')
  })

  it('returns undefined for a real fault', () => {
    expect(ignorableErrorReason(mutedEventFields({
      message: 'TypeError: boom',
      filename: 'app.js',
      lineno: 12,
      error: new Error('boom'),
    }))).toBeUndefined()
  })
})

describe('installIgnorableErrorSuppressor', () => {
  const disposers: Array<() => void> = []

  afterEach(() => {
    while (disposers.length)
      disposers.pop()!()
    vi.restoreAllMocks()
  })

  function install(target?: Parameters<typeof installIgnorableErrorSuppressor>[0]) {
    const dispose = installIgnorableErrorSuppressor(target)
    disposers.push(dispose)
    return dispose
  }

  /** A listener registered AFTER the suppressor, standing in for the dev overlay. */
  function overlay() {
    const listener = vi.fn()
    window.addEventListener('error', listener)
    disposers.push(() => window.removeEventListener('error', listener))
    return listener
  }

  function dispatchError(message: string): ErrorEvent {
    // jsdom's programmatic dispatch does not implement ErrorEvent's default
    // action, so preventDefault() cannot flip defaultPrevented here. Assert
    // downstream-listener suppression (stopImmediatePropagation) instead, which
    // is the load-bearing behavior for the dev overlay, and spy on
    // preventDefault where the call itself is what matters.
    const event = new ErrorEvent('error', { message, cancelable: true })
    window.dispatchEvent(event)
    return event
  }

  it('stops a later window error listener (the dev overlay) from seeing the RO loop error', () => {
    install()
    const overlayListener = overlay()

    dispatchError('ResizeObserver loop completed with undelivered notifications')

    expect(overlayListener).not.toHaveBeenCalled()
  })

  // The iOS Safari case: tapping Share resizes and snapshots the page, and the
  // resulting error reaches JS with every field stripped. Before this, the
  // message-only RO filter let it through and the overlay showed a bare
  // "Script error." over a working app.
  it('stops the dev overlay from seeing a muted error', () => {
    install()
    const overlayListener = overlay()

    dispatchError('Script error.')

    expect(overlayListener).not.toHaveBeenCalled()
  })

  it('lets unrelated errors reach a later listener', () => {
    install()
    const overlayListener = overlay()

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

  // preventDefault() also suppresses the browser's own console report. For a
  // muted error that console line is the ONLY place the unsanitized message,
  // file and line survive -- it is what Web Inspector is attached to read -- so
  // the muted class must be hidden from the overlay and from nothing else.
  it('does not call preventDefault on a muted error, so its console report survives', () => {
    install()
    const mutedEvent = new ErrorEvent('error', { message: 'Script error.', cancelable: true })
    const prevent = vi.spyOn(mutedEvent, 'preventDefault')
    const stop = vi.spyOn(mutedEvent, 'stopImmediatePropagation')

    window.dispatchEvent(mutedEvent)

    expect(stop).toHaveBeenCalledTimes(1)
    expect(prevent).not.toHaveBeenCalled()
  })

  it('disposer removes the listener so the overlay sees the error again', () => {
    const dispose = install()
    const overlayListener = overlay()

    dispose()
    dispatchError('ResizeObserver loop completed with undelivered notifications')

    expect(overlayListener).toHaveBeenCalledTimes(1)
  })

  it('logs one rate-limited debug line per window, per reason, with a running count', () => {
    // Full silence would also mask a GENUINE per-frame feedback loop (the browser
    // emits the same message for both); the suppressor keeps that signal observable
    // as at most one console.debug per window, with a count a real loop makes climb.
    // The tallies are per reason so a storm of one class cannot hide the other.
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})
    // Rate limit reads monotonicNow → performance.now().
    const nowSpy = vi.spyOn(performance, 'now')
    nowSpy.mockReturnValue(100_000)
    install()

    dispatchError('ResizeObserver loop limit exceeded')
    dispatchError('ResizeObserver loop limit exceeded')
    dispatchError('ResizeObserver loop limit exceeded')
    expect(debug).toHaveBeenCalledTimes(1) // burst inside the window -> one line
    expect(String(debug.mock.calls[0][0])).toContain('resize-observer-loop')
    expect(String(debug.mock.calls[0][0])).toContain('x1')

    // A different reason inside the same window keeps its own tally and logs.
    dispatchError('Script error.')
    expect(debug).toHaveBeenCalledTimes(2)
    expect(String(debug.mock.calls[1][0])).toContain('muted')
    expect(String(debug.mock.calls[1][0])).toContain('x1')

    nowSpy.mockReturnValue(115_000) // past the 10s window
    dispatchError('ResizeObserver loop limit exceeded')
    expect(debug).toHaveBeenCalledTimes(3)
    expect(String(debug.mock.calls[2][0])).toContain('x4') // count kept climbing

    dispatchError('nope') // unrelated errors never log
    expect(debug).toHaveBeenCalledTimes(3)
  })

  it('is a no-op when there is no DOM (SSR)', () => {
    // Stub the global so defaultTarget() resolves to undefined (typeof window ===
    // 'undefined'). Calling with no argument then hits the no-op branch. Passing
    // an explicit `undefined` would NOT test this -- it re-triggers the default
    // parameter, which resolves back to window in jsdom.
    vi.stubGlobal('window', undefined)
    try {
      const dispose = installIgnorableErrorSuppressor()
      expect(() => dispose()).not.toThrow()
    }
    finally {
      vi.unstubAllGlobals()
    }
  })
})
