import { describe, expect, it } from 'vitest'
import { cannotLeaveStickyBand, maxScrollTopOf, STICKY_BOTTOM_THRESHOLD_PX } from './chatScrollGeometry'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

describe('cannotleavestickyband', () => {
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
