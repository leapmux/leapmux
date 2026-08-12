import { describe, expect, it } from 'vitest'
import { cannotLeaveStickyBand, hasScrollRoom, maxScrollTopOf, STICKY_BOTTOM_THRESHOLD_PX } from './chatScrollGeometry'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

describe('cannotLeaveStickyBand', () => {
  const paneOf = (scrollHeight: number, clientHeight: number) => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(clientHeight)
    div.setScrollHeight(scrollHeight)
    return div.el
  }

  it('is true when the content fits (no scrollable range)', () => {
    // maxScrollTop 0: every scroll position is the bottom.
    expect(cannotLeaveStickyBand(paneOf(500, 500))).toBe(true)
    expect(cannotLeaveStickyBand(paneOf(400, 500))).toBe(true) // clamps to 0
  })

  it('is true at or below the sticky-band threshold, false just past it', () => {
    // The bound is the sticky band, not a strict 0: a barely-scrollable page whose whole
    // range fits inside the band can never scroll OUT of it, so it must still unwedge.
    const atThreshold = paneOf(500 + STICKY_BOTTOM_THRESHOLD_PX, 500)
    expect(maxScrollTopOf(atThreshold)).toBe(STICKY_BOTTOM_THRESHOLD_PX)
    expect(cannotLeaveStickyBand(atThreshold)).toBe(true)

    const justPast = paneOf(500 + STICKY_BOTTOM_THRESHOLD_PX + 1, 500)
    expect(cannotLeaveStickyBand(justPast)).toBe(false)
  })

  it('is false for a comfortably scrollable pane', () => {
    expect(cannotLeaveStickyBand(paneOf(50000, 500))).toBe(false)
  })
})

describe('hasScrollRoom', () => {
  /** A scroller of `scrollHeight` in a `clientHeight` box, parked at `scrollTop`. */
  const scrollerAt = (scrollHeight: number, clientHeight: number, scrollTop: number) => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(clientHeight)
    div.setScrollHeight(scrollHeight)
    div.el.scrollTop = scrollTop
    return div.el
  }

  it('reads room in the direction the wheel actually goes', () => {
    // 600 of content in a 400 box = 200px of travel, and the reader sits halfway down it. Both
    // directions have room, and a formula that measured only one would be wrong for the other.
    const midway = scrollerAt(600, 400, 100)
    expect(hasScrollRoom(midway, 50)).toBe(true) // down
    expect(hasScrollRoom(midway, -50)).toBe(true) // up
  })

  it('has none downward at the bottom, and none upward at the top', () => {
    const atBottom = scrollerAt(600, 400, 200)
    expect(hasScrollRoom(atBottom, 50)).toBe(false)
    expect(hasScrollRoom(atBottom, -50)).toBe(true) // the whole travel is still above it

    const atTop = scrollerAt(600, 400, 0)
    expect(hasScrollRoom(atTop, -50)).toBe(false)
    expect(hasScrollRoom(atTop, 50)).toBe(true)
  })

  it('has no room in either direction when the content fits', () => {
    const fits = scrollerAt(200, 400, 0)
    expect(hasScrollRoom(fits, 50)).toBe(false)
    expect(hasScrollRoom(fits, -50)).toBe(false)
  })

  it('ignores sub-pixel travel, which no reader can use', () => {
    // Fractional DPI and browser zoom leave a sliver on a scroller that is already at its limit.
    // Reporting it as room would trap the wheel at the end of the card instead of chaining out.
    const sliverLeft = scrollerAt(600.4, 400, 200)
    expect(hasScrollRoom(sliverLeft, 50)).toBe(false)
    expect(hasScrollRoom(scrollerAt(600.4, 400, 199), 50)).toBe(true) // a whole pixel does count
  })

  it('reads the SIGN of the delta only, so a line/page-mode wheel needs no conversion first', () => {
    // deltaMode scales the magnitude, never the sign, which is why the card's wheel handler can
    // skip the deltaMode normalization the rail's own forwarder has to do.
    const midway = scrollerAt(600, 400, 100)
    expect(hasScrollRoom(midway, 1)).toBe(hasScrollRoom(midway, 10_000))
    expect(hasScrollRoom(midway, -1)).toBe(hasScrollRoom(midway, -10_000))
  })
})
