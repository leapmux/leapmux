import { describe, expect, it } from 'vitest'
import { createProgrammaticScrollGuard } from './chatScrollProgrammaticGuard'

/** A fake scroll element whose scrollTop the test controls. */
function fakeEl(scrollTop = 0) {
  return { scrollTop } as HTMLDivElement
}

/** A manual clock the test advances explicitly (ms). */
function fakeClock() {
  let t = 1000
  const now = () => t
  const advance = (ms: number) => {
    t += ms
  }
  return { now, advance }
}

describe('createprogrammaticscrollguard', () => {
  it('recognizes the echoing scroll event at the written position', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(500)
    expect(el.scrollTop).toBe(500)
    expect(g.isEcho()).toBe(true) // the echo lands at 500
  })

  it('still recognizes an echo delivered SEVERAL frames late (within the TTL) -- the S8 fix', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(500)
    // The browser delivers the scroll event ~5 frames late (a fixed one-rAF deadline
    // would have already cleared the marker, misreading this as a user gesture).
    clock.advance(80)
    expect(g.isEcho()).toBe(true)
  })

  it('ages the marker out after the TTL so a much-later scroll to the same pixel is a real gesture', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(500)
    clock.advance(200) // > ECHO_MARKER_TTL_MS (150)
    expect(g.isEcho()).toBe(false)
  })

  it('treats a scroll landing away from the written position as a genuine gesture', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(500)
    el.scrollTop = 460 // user scrolled mid-burst, more than 1px off our write
    expect(g.isEcho()).toBe(false)
  })

  it('consumeEcho clears the matched marker but spares a fresher one armed mid-handler', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(500)
    const gen = g.matchedEchoGen() // this event's marker, snapshotted before the re-stick
    // A re-stick during the handler issues a SECOND write (a fresher marker at 800).
    g.write(800)
    // Consuming the FIRST event's marker must not clear the second write's pending echo.
    g.consumeEcho(gen)
    expect(g.isEcho()).toBe(true) // still recognizes the 800 echo

    // Consuming the marker the current position matches clears it.
    g.consumeEcho(g.matchedEchoGen())
    expect(g.isEcho()).toBe(false)
  })

  it('keeps two pending echoes at the SAME pixel distinct (FIFO), not stranding the second (S9)', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    // Two programmatic writes landing on the SAME pixel in separate frames (a write plus
    // a re-pin to the same spot): each arms its OWN marker, each with its echo still in
    // flight. A single-slot guard would clobber the first, so consuming one echo would
    // strand the other to be read as a user gesture.
    g.write(300)
    el.scrollTop = 300 // a second programmatic write via a different path, same pixel
    g.mark()

    // First echo: consume the OLDEST matching marker (FIFO). The second echo must STILL
    // be recognized.
    const firstGen = g.matchedEchoGen()
    g.consumeEcho(firstGen)
    expect(g.isEcho()).toBe(true)
    const secondGen = g.matchedEchoGen()
    expect(secondGen).not.toBe(firstGen)
    g.consumeEcho(secondGen)
    expect(g.isEcho()).toBe(false)
  })

  it('feeds each programmatic write position to onMark (velocity baseline)', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const marks: number[] = []
    const g = createProgrammaticScrollGuard(() => el, p => marks.push(p), clock.now)

    g.write(120)
    g.write(340)
    expect(marks).toEqual([120, 340])
  })

  it('does NOT arm a marker for a write that lands on the current position (no echo)', () => {
    // A write to the position scrollTop is already at fires no scroll event, so a
    // marker would never be consumed and would instead linger for the TTL, swallowing
    // a genuine user scroll that happens to land within 1px of that pixel.
    const el = fakeEl(500)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(500) // no movement -- already at 500
    expect(g.isEcho()).toBe(false)
  })

  it('only marks (and feeds onMark) for writes that actually move scrollTop', () => {
    const el = fakeEl(100)
    const clock = fakeClock()
    const marks: number[] = []
    const g = createProgrammaticScrollGuard(() => el, p => marks.push(p), clock.now)

    g.write(100) // no move
    g.write(250) // moved
    expect(marks).toEqual([250])
  })

  it('exposes marker sources for diagnostics', () => {
    const el = fakeEl(0)
    const clock = fakeClock()
    const g = createProgrammaticScrollGuard(() => el, undefined, clock.now)

    g.write(120, 'anchor-repin')
    clock.advance(25)
    expect(g.debugMarkers()).toEqual([
      { top: 120, ageMs: 25, gen: 1, source: 'anchor-repin' },
    ])
  })
})
