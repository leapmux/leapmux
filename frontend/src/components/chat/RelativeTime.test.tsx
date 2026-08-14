import { cleanup, render, screen } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RelativeTime, RelativeTimeAgo } from './RelativeTime'

describe('relativeTime', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-05-01T12:00:00Z'))
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  function iso(secondsAgo: number): string {
    return new Date(Date.now() - secondsAgo * 1000).toISOString()
  }

  it('renders the compact elapsed time', () => {
    render(() => <RelativeTime timestamp={iso(90)} />)
    expect(screen.getByText(/1m/)).toBeTruthy()
  })

  it('renders nothing for an empty or unparseable timestamp', () => {
    const { container } = render(() => <RelativeTime timestamp="" />)
    expect(container.textContent?.trim()).toBe('')
    const bad = render(() => <RelativeTime timestamp="not a date" />)
    expect(bad.container.textContent?.trim()).toBe('')
  })

  it('appends " ago" in the RelativeTimeAgo form', () => {
    const { container } = render(() => <RelativeTimeAgo timestamp={iso(120)} />)
    expect(container.textContent).toContain('2m')
    expect(container.textContent).toContain('ago')
  })

  /**
   * The refresh runs on ONE shared interval rather than one per instance, so
   * this pins that every mounted instance still updates from it.
   */
  it('refreshes every mounted instance from the shared tick', () => {
    // Hoisted, NOT inlined into the JSX: Solid compiles a call expression in a
    // prop into a getter, so `timestamp={iso(10)}` would re-read Date.now() on
    // every tick and the elapsed time would never appear to move.
    const tenSecondsAgo = iso(10)
    const twentySecondsAgo = iso(20)
    const { container } = render(() => (
      <>
        <RelativeTime timestamp={tenSecondsAgo} />
        <RelativeTime timestamp={twentySecondsAgo} />
      </>
    ))
    expect(container.textContent).toContain('10s')
    expect(container.textContent).toContain('20s')

    vi.advanceTimersByTime(15_000)

    expect(container.textContent).toContain('25s')
    expect(container.textContent).toContain('35s')
  })

  /**
   * `onCleanup` runs whenever the owner is disposed — including before the
   * effect queue ever flushes `onMount`. An unpaired decrement would drive the
   * module-level subscriber count negative, after which the "first subscriber"
   * check never matches again and NO later instance gets a timer for the rest
   * of the session.
   */
  it('survives an owner disposed before its mount effect ever ran', () => {
    const tenSecondsAgo = iso(10)

    createRoot((dispose) => {
      // Calling the component runs its body — registering both hooks — while
      // the immediate dispose runs cleanup with the mount effect still queued.
      RelativeTime({ timestamp: tenSecondsAgo })
      dispose()
    })

    const { container } = render(() => <RelativeTime timestamp={tenSecondsAgo} />)
    expect(container.textContent).toContain('10s')

    vi.advanceTimersByTime(15_000)

    expect(container.textContent, 'the shared timer must still start').toContain('25s')
  })

  it('stops the shared interval once the last instance unmounts', () => {
    const clearSpy = vi.spyOn(globalThis, 'clearInterval')
    const first = render(() => <RelativeTime timestamp={iso(5)} />)
    const second = render(() => <RelativeTime timestamp={iso(5)} />)

    first.unmount()
    expect(clearSpy).not.toHaveBeenCalled()

    second.unmount()
    expect(clearSpy).toHaveBeenCalled()
    clearSpy.mockRestore()
  })
})
