import type { ScrollActivity, ScrollActivityDeps } from './chatScrollActivity'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createScrollActivity } from './chatScrollActivity'

const IDLE_MS = 400

let disposeRoot: (() => void) | undefined

/**
 * Fake BOTH `performance` and the timers, not just the timers. `createScrollActivity`
 * defaults its clock to `monotonicNow`, which reads `performance.now()`, so a plain
 * `vi.useFakeTimers()` would leave the idle deadline comparison on the wall clock while
 * the idle timer ran on the fake one -- every deadline assertion would then pass or fail on
 * machine speed. Faking both moves them together under one `advanceTimersByTime`, and
 * starts the clock at exactly 0, which is the epoch the zero-epoch test below needs.
 * (`useChatScroll.diagnostics.test.ts` uses the same form.)
 */
function setup(deps: Partial<ScrollActivityDeps> = {}): ScrollActivity {
  vi.useFakeTimers({
    toFake: ['performance', 'setTimeout', 'clearTimeout'],
  })
  return createRoot((dispose) => {
    disposeRoot = dispose
    return createScrollActivity({ idleMs: IDLE_MS, ...deps })
  })
}

afterEach(() => {
  disposeRoot?.()
  disposeRoot = undefined
  vi.useRealTimers()
})

describe('createScrollActivity', () => {
  it('starts inactive with no timer armed', () => {
    const activity = setup()

    expect(activity.active()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('opens the window synchronously on a user input', () => {
    const activity = setup()

    activity.noteInput()

    // Synchronously, not one frame later: the rail must light on touchstart, before the
    // finger has moved far enough for the reader to wonder where the scrollbar went.
    expect(activity.active()).toBe(true)
  })

  it('closes the window exactly idleMs after the last input', () => {
    const activity = setup()

    activity.noteInput()
    vi.advanceTimersByTime(IDLE_MS - 1)
    expect(activity.active()).toBe(true)

    vi.advanceTimersByTime(1)
    expect(activity.active()).toBe(false)
  })

  it('restarts the idle window on each input rather than expiring from the first', () => {
    const activity = setup()

    activity.noteInput()
    vi.advanceTimersByTime(IDLE_MS - 10)
    activity.noteInput()
    vi.advanceTimersByTime(IDLE_MS - 10)
    // A throttle would have closed at IDLE_MS; a debounce measures from the second input.
    expect(activity.active()).toBe(true)

    vi.advanceTimersByTime(10)
    expect(activity.active()).toBe(false)
  })

  it('never opens the window on a scroll event alone', () => {
    const activity = setup()

    activity.noteScroll()

    expect(activity.active()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('never opens the window on a scroll event before any input, on a zero-epoch clock', () => {
    // A tab can receive a scroll in its first tick while it restores its position.
    const activity = setup()
    expect(performance.now()).toBe(0)

    activity.noteScroll()

    expect(activity.active()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('extends the window from scroll movement while the window is open', () => {
    const activity = setup()

    activity.noteInput()
    vi.advanceTimersByTime(IDLE_MS - 1)
    // A touch flick's coast: no touch or pointer event fires, only `scroll`.
    activity.noteScroll()

    vi.advanceTimersByTime(IDLE_MS - 1)
    expect(activity.active()).toBe(true)

    vi.advanceTimersByTime(1)
    expect(activity.active()).toBe(false)
  })

  it('keeps the window open throughout continuous momentum', () => {
    const activity = setup()
    activity.noteInput()

    for (let step = 0; step < 8; step++) {
      vi.advanceTimersByTime(IDLE_MS - 100)
      activity.noteScroll()
      expect(activity.active()).toBe(true)
    }

    vi.advanceTimersByTime(IDLE_MS)
    expect(activity.active()).toBe(false)
  })

  it('keeps the window closed during scroll events after it fades', () => {
    const activity = setup()

    activity.noteInput()
    vi.advanceTimersByTime(IDLE_MS)
    expect(activity.active()).toBe(false)

    // A streaming turn's stick-to-bottom writes: a long burst of scroll events with no user
    // input behind them. Assert INSIDE the loop -- checking only after would pass even if
    // the window flickered open on every one of them.
    for (let i = 0; i < 10; i++) {
      vi.advanceTimersByTime(16)
      activity.noteScroll()
      expect(activity.active()).toBe(false)
      expect(vi.getTimerCount()).toBe(0)
    }
  })

  it('ignores movement past the deadline while the idle callback waits', () => {
    let now = 0
    const activity = setup({
      now: () => now,
    })
    activity.noteInput()
    vi.advanceTimersByTime(IDLE_MS - 1)

    now = IDLE_MS
    activity.noteScroll()
    vi.advanceTimersByTime(1)

    expect(activity.active()).toBe(false)
  })

  it('closes on the next tick with an idle window of zero, neither synchronously nor never', () => {
    const activity = setup({ idleMs: 0 })

    activity.noteInput()
    expect(activity.active()).toBe(true)

    vi.advanceTimersByTime(0)
    expect(activity.active()).toBe(false)
  })

  it('cancels the pending timer on dispose and ignores every later event', () => {
    const activity = setup()

    activity.noteInput()
    expect(vi.getTimerCount()).toBe(1)

    activity.dispose()
    expect(activity.active()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)

    // A passive touchend can still land after the owner tears down; it must not arm a timer
    // on a disposed component.
    activity.noteInput()
    activity.noteScroll()
    expect(activity.active()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('disposes itself when its owner disposes', () => {
    const activity = setup()
    activity.noteInput()
    expect(vi.getTimerCount()).toBe(1)

    disposeRoot?.()
    disposeRoot = undefined

    expect(vi.getTimerCount()).toBe(0)
    activity.noteInput()
    expect(activity.active()).toBe(false)
  })

  it('keeps two instances with different idle windows independent', () => {
    // The shape ChatView actually builds: a short window for the syntax-highlight pause and
    // a long one for the rail. A module-level timer or timestamp would couple them.
    vi.useFakeTimers({
      toFake: ['performance', 'setTimeout', 'clearTimeout'],
    })
    const { short, long } = createRoot((dispose) => {
      disposeRoot = dispose
      return {
        short: createScrollActivity({ idleMs: 160 }),
        long: createScrollActivity({ idleMs: IDLE_MS }),
      }
    })

    short.noteInput()
    long.noteInput()

    vi.advanceTimersByTime(160)
    expect(short.active()).toBe(false)
    expect(long.active()).toBe(true)

    vi.advanceTimersByTime(IDLE_MS - 160)
    expect(long.active()).toBe(false)
  })
})
