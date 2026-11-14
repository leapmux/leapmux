import { describe, expect, it } from 'vitest'
import { installScrollTestEnv, mountScrollHarness } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500. The hook
// follows the tail at the mount. `changeAbove(px)` grows row 0 and moves the layer by
// `-px`, as the virtualizer keeps the rows under the reader in place.

describe('useChatScroll geometry change', () => {
  it('writes no scroll position for a change above the viewport during a scroll', () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    const writes = h.div.scrollTopWrites()

    h.changeAbove(60)

    expect(h.div.getScrollTop()).toBe(1990)
    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.rebaseCount()).toBe(0)
    expect(h.virt.layoutShift()).toBe(-60)
    h.dispose()
  })

  it('writes no scroll position while a pointer is down', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))

    h.changeAbove(60)
    await h.rest()
    await h.rest()

    expect(h.rebaseCount()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('rebases after the change when the scroller is at rest', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)

    h.changeAbove(60)

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('rebases when the pointer goes up after a change during the press', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(0)

    h.hook.handlers.onPointerUp(new PointerEvent('pointerup', { pointerId: 1, isPrimary: true }))

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })

  it('refreshes the rendered rows in a later frame, not in the effect', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.updateViewport.mockClear()

    h.changeAbove(60)
    expect(h.updateViewport).not.toHaveBeenCalled()

    await h.frame()
    expect(h.updateViewport).toHaveBeenCalledOnce()
    h.dispose()
  })

  it('returns to the same row after the pane was hidden', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.changeAbove(60)

    await h.hideThenShow()

    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })
})
