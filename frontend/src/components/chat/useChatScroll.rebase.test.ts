import { describe, expect, it, vi } from 'vitest'
import { SCROLL_REST_QUIET_MS } from './chatScrollRest'
import { installScrollTestEnv, mountScrollHarness } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500. `changeAbove(px)`
// grows row 0 and moves the layer by `-px`; a negative `px` shrinks row 0 and moves the
// layer down. A rebase removes the shift: `scrollTop` changes by `-shift`.

/** The rows of the next tests: one tall row and ten rows of 100px. */
const TALL_FIRST_ROW = [
  4000,
  ...Array.from<number>({ length: 10 }).fill(100),
]

/** Scroll to 450 after a large change far from the top: the rebase that comes near the top. */
function scrolledNearTheTop() {
  const h = mountScrollHarness()
  h.scrollTo(2000)
  h.scrollTo(1990)
  h.changeAbove(5000)
  expect(h.rebaseCount()).toBe(0)
  h.scrollTo(450)
  return h
}

describe('useChatScroll rebase', () => {
  it('rebases after the quiet time', async () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(60)

    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    expect(h.rebaseCount()).toBe(0)
    await h.frame()

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2050)
    h.dispose()
  })

  it('does not rebase when the position changed before the confirm frame', async () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(60)

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
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(60)
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

  it('recovers after a dropped pointerup', () => {
    const h = mountScrollHarness()
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 2, isPrimary: true }))
    h.hook.handlers.onPointerUp(new PointerEvent('pointerup', { pointerId: 2, isPrimary: true }))

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
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(5000)
    expect(h.rebaseCount()).toBe(0)
    h.dispose()
  })

  it('rebases when the scroll comes near the top', () => {
    const h = scrolledNearTheTop()
    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(5450)
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
    h.scrollTo(30)
    h.changeAbove(-50)
    expect(h.rebaseCount()).toBe(1)
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
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(60)

    h.hook.pageScroll(1)

    expect(h.rebaseCount()).toBe(1)
    // 1990 + 60 = 2050, then a page of 500 - 48 = 452.
    expect(h.div.getScrollTop()).toBe(2502)
    h.dispose()
  })

  it('does not rebase for a stick to the bottom', () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(60)

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
