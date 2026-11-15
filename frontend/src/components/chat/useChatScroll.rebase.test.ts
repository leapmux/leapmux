import { describe, expect, it, vi } from 'vitest'
import { SCROLL_REST_QUIET_MS } from './chatScrollRest'
import { installScrollTestEnv, mountScrollHarness } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500. While the
// viewport top lies below row 0, `changeAbove(px)` grows row 0 and moves the layer by `-px`;
// a negative `px` shrinks row 0 and moves the layer down. A rebase removes the shift:
// `scrollTop` changes by `-shift`.

/** The rows of the next tests: one tall row and ten rows of 100px. */
const TALL_FIRST_ROW = [
  4000,
  ...Array.from<number>({ length: 10 }).fill(100),
]

/**
 * A reader who scrolls up from 2000 to 1990, and a change of `px` above the viewport during
 * that scroll: the shift is `-px`, and the scroller does not rest yet.
 */
function scrolledUpWithChange(px: number) {
  const h = mountScrollHarness()
  h.scrollTo(2000)
  h.scrollTo(1990)
  h.changeAbove(px)
  return h
}

/** Scroll to 450 after a large change far from the top: the rebase that comes near the top. */
function scrolledNearTheTop() {
  const h = scrolledUpWithChange(5000)
  expect(h.rebaseCount()).toBe(0)
  h.scrollTo(450)
  return h
}

describe('useChatScroll rebase', () => {
  it('reports no placement to the virtualizer for a rebase', async () => {
    const h = scrolledUpWithChange(60)

    await h.rest()

    // The rebase wrote `scrollTop`, and nothing moved on screen.
    expect(h.rebaseCount()).toBe(1)
    expect(h.notePlacement).not.toHaveBeenCalled()
    h.dispose()
  })

  it('rebases after the quiet time', async () => {
    const h = scrolledUpWithChange(60)

    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    expect(h.rebaseCount()).toBe(0)
    await h.frame()

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2050)
    h.dispose()
  })

  it('does not rebase when the position changed before the confirm frame', async () => {
    const h = scrolledUpWithChange(60)

    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    // A `scroll` event is still on its way.
    h.div.setScrollTop(1980)
    await h.frame()
    expect(h.rebaseCount()).toBe(0)

    h.hook.handlers.onScroll()
    await h.rest()
    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('does not rest during a rubber-band overscroll', async () => {
    const h = scrolledUpWithChange(60)
    h.scrollTo(0)
    for (const raw of [-20, -35, -15]) {
      vi.advanceTimersByTime(100)
      h.div.setRawScrollTop(raw)
      h.hook.handlers.onScroll()
      await h.frame()
    }
    expect(h.rebaseCount()).toBe(0)

    h.div.setRawScrollTop(0)
    h.hook.handlers.onScroll()
    await h.rest()

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(60)
    h.dispose()
  })

  it('does not rebase while a touch is down', async () => {
    const h = mountScrollHarness()
    h.hook.handlers.onTouchStart({
      touches: [
        { clientY: 10 },
      ],
    } as unknown as TouchEvent)
    h.scrollTo(1990)
    h.changeAbove(60)
    await h.rest()
    await h.rest()

    expect(h.rebaseCount()).toBe(0)
    h.dispose()
  })

  it('rebases after the last touch ends', async () => {
    const h = mountScrollHarness()
    h.hook.handlers.onTouchStart({
      touches: [
        { clientY: 10 },
      ],
    } as unknown as TouchEvent)
    h.scrollTo(1990)
    h.changeAbove(60)
    await h.rest()
    await h.rest()

    h.hook.handlers.onTouchEnd({ touches: [] } as unknown as TouchEvent)

    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('rebases after the browser cancels the last touch', async () => {
    const h = mountScrollHarness()
    h.hook.handlers.onTouchStart({
      touches: [
        { clientY: 10 },
      ],
    } as unknown as TouchEvent)
    h.scrollTo(1990)
    h.changeAbove(60)
    await h.rest()
    await h.rest()
    expect(h.rebaseCount()).toBe(0)

    h.hook.handlers.onTouchCancel({ touches: [] } as unknown as TouchEvent)

    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('keeps the touch active while a second finger remains', async () => {
    const h = mountScrollHarness()
    h.hook.handlers.onTouchStart({
      touches: [
        { clientY: 10 },
        { clientY: 40 },
      ],
    } as unknown as TouchEvent)
    h.scrollTo(1990)
    h.changeAbove(60)
    await h.rest()
    await h.rest()

    // `touches` lists the fingers that are still down after this one lifted.
    h.hook.handlers.onTouchEnd({
      touches: [
        { clientY: 40 },
      ],
    } as unknown as TouchEvent)
    expect(h.rebaseCount()).toBe(0)

    h.hook.handlers.onTouchEnd({ touches: [] } as unknown as TouchEvent)
    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('rests after a pointer that is released outside the list', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(0)

    document.body.dispatchEvent(new PointerEvent('pointerup', { pointerId: 1, bubbles: true }))

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })

  it('rests when a context menu takes the release of a mouse pointer', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true, pointerType: 'mouse' }))
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(0)

    // The native menu of a right press takes the pointerup.
    document.body.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })

  it('keeps a finger down through the context menu of a long press', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 2, isPrimary: true, pointerType: 'touch' }))
    h.changeAbove(60)

    document.body.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))
    await h.rest()

    expect(h.rebaseCount()).toBe(0)
    h.dispose()
  })

  it('rests when a pointer loses its capture with no button down', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true, pointerType: 'pen' }))
    h.changeAbove(60)

    // A pen keeps its button down when the row that captured it unmounts: no release.
    document.body.dispatchEvent(new PointerEvent('lostpointercapture', { pointerId: 1, buttons: 1, bubbles: true }))
    await h.rest()
    expect(h.rebaseCount()).toBe(0)

    document.body.dispatchEvent(new PointerEvent('lostpointercapture', { pointerId: 1, buttons: 0, bubbles: true }))
    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('rests when the window loses the focus during a press', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true, pointerType: 'mouse' }))
    h.changeAbove(60)

    // The release goes to another window.
    window.dispatchEvent(new FocusEvent('blur'))

    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('recovers after a dropped pointerup', () => {
    const h = mountScrollHarness()
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 2, isPrimary: true }))
    document.body.dispatchEvent(new PointerEvent('pointerup', { pointerId: 2, isPrimary: true, bubbles: true }))

    h.changeAbove(60)

    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('rebases a large shift at once near the reachable top', () => {
    const h = mountScrollHarness()
    h.scrollTo(400)
    h.changeAbove(600)
    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(1000)
    h.dispose()
  })

  it('does not rebase a small shift near the reachable top during a scroll', async () => {
    const h = mountScrollHarness()
    h.scrollTo(400)
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(0)
    expect(h.div.getScrollTop()).toBe(400)

    await h.rest()
    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(460)
    h.dispose()
  })

  it('does not rebase far from the top during a scroll', () => {
    const h = scrolledUpWithChange(5000)
    expect(h.rebaseCount()).toBe(0)
    h.dispose()
  })

  it('rebases when the scroll comes near the top', () => {
    const h = scrolledNearTheTop()
    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(5450)
    h.dispose()
  })

  it('waits for the event of a move on its way before it rebases near the reachable top', () => {
    const h = scrolledUpWithChange(5000)
    h.onMomentumScroll.mockClear()
    // The browser moved the view near the reachable top, and the `scroll` event of the move
    // did not arrive yet. A row above the viewport measures in between.
    h.div.setScrollTop(450)
    h.changeAbove(10)
    // A rebase now would record 450 as seen, and the event would read as no move.
    expect(h.rebaseCount()).toBe(0)

    h.hook.handlers.onScroll()

    expect(h.onMomentumScroll).toHaveBeenCalledOnce()
    expect(h.rebaseCount()).toBe(1)
    h.dispose()
  })

  it('rebases a large positive shift near the first row', () => {
    const h = mountScrollHarness({ heights: TALL_FIRST_ROW })
    h.scrollTo(4300)
    h.changeAbove(-3900)
    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(400)
    h.dispose()
  })

  it('rebases at once when the viewport top is above the first row', () => {
    const h = mountScrollHarness()
    // The rule holds the point under the viewport top, so a change of the rows never puts
    // the viewport top above the first row. A scroll into the band that a positive shift
    // leaves above the first row does.
    h.scrollTo(400)
    h.changeAbove(-50)
    expect(h.virt.layoutShift()).toBe(50)
    expect(h.rebaseCount()).toBe(0)

    h.scrollTo(30)

    expect(h.rebaseCount()).toBe(1)
    // The rebase writes 30 - 50, and the browser clamps it to 0.
    expect(h.div.getScrollTop()).toBe(0)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('rebases at once in the geometry effect when an estimated first row shrinks under the viewport top', () => {
    // Row 0 has only an estimate of 1000px, and the reader scrolled to 10px into it.
    const h = mountScrollHarness({
      heights: [
        1000,
        ...Array.from<number>({ length: 40 }).fill(100),
      ],
      estimatedRows: [0],
    })
    h.scrollTo(10)
    expect(h.rebaseCount()).toBe(0)

    // Row 0 measures 100px. The rule holds the bottom edge of a row with an estimated height,
    // so the viewport top lands 890px above the first row, in the blank band of a positive
    // shift. The geometry effect rebases at once, although the scroller does not rest.
    h.changeAbove(-900)

    expect(h.rebaseCount()).toBe(1)
    expect(h.virt.layoutShift()).toBe(0)
    // The rebase writes 10 - 900, and the browser clamps it to 0.
    expect(h.div.getScrollTop()).toBe(0)
    h.dispose()
  })

  it('does not rebase a small positive shift below the viewport top during a scroll', () => {
    const h = mountScrollHarness()
    h.scrollTo(400)
    h.changeAbove(-50)
    expect(h.rebaseCount()).toBe(0)
    expect(h.div.getScrollTop()).toBe(400)
    h.dispose()
  })

  it('translates an old-coordinate event after a rebase during a scroll', () => {
    const h = scrolledNearTheTop()
    // WebKit delivers a momentum event in the coordinates before the rebase.
    h.div.setScrollTop(440)
    h.hook.handlers.onScroll()
    expect(h.div.getScrollTop()).toBe(5440)
    h.dispose()
  })

  it('translates an old-coordinate event that arrives after the event of the rebase', () => {
    const h = scrolledNearTheTop()
    expect(h.div.getScrollTop()).toBe(5450)
    // The event of the rebase's own write arrives first, and reads as no move.
    h.hook.handlers.onScroll()
    expect(h.div.getScrollTop()).toBe(5450)

    // WebKit then delivers a momentum event in the coordinates before the rebase.
    h.div.setScrollTop(440)
    h.hook.handlers.onScroll()

    expect(h.div.getScrollTop()).toBe(5440)
    h.dispose()
  })

  it('joins a second rebase to the record of the first', () => {
    const h = scrolledNearTheTop()
    h.changeAbove(-4000)
    expect(h.div.getScrollTop()).toBe(1450)

    h.div.setScrollTop(440)
    h.hook.handlers.onScroll()

    expect(h.rebaseCount()).toBe(2)
    // The translator adds the distance of both rebases: 5000 - 4000.
    expect(h.div.getScrollTop()).toBe(1440)
    h.dispose()
  })

  it('does not translate after a rebase at rest', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.changeAbove(5000)
    expect(h.div.getScrollTop()).toBe(7000)

    h.div.setScrollTop(1990)
    h.hook.handlers.onScroll()

    expect(h.div.getScrollTop()).toBe(1990)
    h.dispose()
  })

  it('rebases before a placement', () => {
    const h = scrolledUpWithChange(60)

    h.hook.pageScroll(1)

    expect(h.rebaseCount()).toBe(1)
    // 1990 + 60 = 2050, then a page of 500 - 48 = 452.
    expect(h.div.getScrollTop()).toBe(2502)
    h.dispose()
  })

  it('does not rebase for a stick to the bottom', () => {
    const h = scrolledUpWithChange(60)

    h.hook.forceScrollToBottom()

    expect(h.rebaseCount()).toBe(0)
    expect(h.div.getScrollTop()).toBe(h.div.el.scrollHeight - h.div.el.clientHeight)
    h.dispose()
  })

  it('treats the scroll event of a rebase as no move', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.onMomentumScroll.mockClear()

    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(1)
    h.hook.handlers.onScroll()

    expect(h.onMomentumScroll).not.toHaveBeenCalled()
    h.dispose()
  })
})
