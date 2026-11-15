import { describe, expect, it } from 'vitest'
import { cannotScroll, FOLLOW_TOLERANCE_PX, hasScrollRoom, maxScrollTopOf, readLogicalScrollTop } from './chatScrollGeometry'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

describe('cannotScroll', () => {
  const paneOf = (scrollHeight: number, clientHeight: number) => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(clientHeight)
    div.setScrollHeight(scrollHeight)
    return div.el
  }

  it('returns true when the content fits', () => {
    expect(cannotScroll(paneOf(500, 500))).toBe(true)
    expect(cannotScroll(paneOf(400, 500))).toBe(true)
  })

  it('returns true for a range of 2px', () => {
    const pane = paneOf(502, 500)
    expect(maxScrollTopOf(pane)).toBe(FOLLOW_TOLERANCE_PX)
    expect(cannotScroll(pane)).toBe(true)
  })

  it('returns false for a range of 3px', () => {
    expect(cannotScroll(paneOf(503, 500))).toBe(false)
  })
})

describe('readLogicalScrollTop', () => {
  /** A 500px box over 2000px of content that reports a raw `scrollTop`. */
  const scrollerReading = (rawTop: number) => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(500)
    div.setScrollHeight(2000)
    div.setRawScrollTop(rawTop)
    return div.el
  }

  it('returns a position inside the range unchanged', () => {
    expect(readLogicalScrollTop(scrollerReading(700.5))).toBe(700.5)
  })

  it('clamps a rubber-band overscroll to the range', () => {
    expect(readLogicalScrollTop(scrollerReading(-40))).toBe(0)
    expect(readLogicalScrollTop(scrollerReading(1560))).toBe(1500)
  })

  it('does not write the position', () => {
    const el = scrollerReading(-40)
    readLogicalScrollTop(el)
    expect(el.scrollTop).toBe(-40)
  })

  it('returns 0 for any position when the content fits the box', () => {
    // 300px of content in a 500px box: the scroll range holds 0 alone.
    for (const rawTop of [-40, 0, 25]) {
      const div = makeFakeScrollDiv()
      div.setClientHeight(500)
      div.setScrollHeight(300)
      div.setRawScrollTop(rawTop)
      expect(readLogicalScrollTop(div.el)).toBe(0)
    }
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
