import { describe, expect, it, vi } from 'vitest'
import { applyRebaseWrite, placementScrollTop, SCROLL_ROUNDING_MAX_PX } from './createLayoutShift'

/**
 * A scroll element in a 500px view over a spacer of `total + layout` px, as ChatView builds
 * it: `layout` is the shift that the last `writeLayout` wrote, at first `shift`. The element
 * clamps each written `scrollTop` to its scroll range, and then rounds it with `round`, as a
 * browser stores it on its grid of device pixels. A spacer that shrinks below the position
 * clamps the position too. With `staticRange`, the layout writes do not change the scroll
 * range, as with no spacer attached. `writes` records the kind of each write, in order.
 */
function fakeScroller(init: {
  scrollTop: number
  shift: number
  total?: number
  round?: (top: number) => number
  staticRange?: boolean
}) {
  const clientHeight = 500
  const total = init.total ?? 2380
  const round = init.round ?? ((top: number) => top)
  const state = { scrollTop: init.scrollTop, layout: init.shift }
  const writes: Array<'scrollTop' | 'layout'> = []
  const rangeShift = () => (init.staticRange === true
    ? init.shift
    : state.layout)
  const maxTop = () => Math.max(0, total + rangeShift() - clientHeight)
  const el = document.createElement('div')
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => state.scrollTop,
    set: (top: number) => {
      writes.push('scrollTop')
      state.scrollTop = round(Math.min(Math.max(top, 0), maxTop()))
    },
  })
  const writeLayout = vi.fn((shift: number) => {
    writes.push('layout')
    state.layout = shift
    state.scrollTop = Math.min(state.scrollTop, maxTop())
  })
  /** The content coordinate of the viewport top: `scrollTop - shift`, with no inset. */
  const contentTop = () => state.scrollTop - state.layout
  return {
    el,
    state,
    writes,
    writeLayout,
    contentTop,
  }
}

describe('applyRebaseWrite', () => {
  it.each([
    0,
    0.6,
    -0.6,
    SCROLL_ROUNDING_MAX_PX - 0.01,
    -(SCROLL_ROUNDING_MAX_PX - 0.01),
  ])('keeps the shift %s, below SCROLL_ROUNDING_MAX_PX, with no write', (shift) => {
    const { el, state, writes, writeLayout } = fakeScroller({ scrollTop: 1250, shift })

    expect(applyRebaseWrite(el, shift, writeLayout)).toBe(shift)

    expect(writes).toEqual([])
    expect(state.scrollTop).toBe(1250)
  })

  it.each([
    SCROLL_ROUNDING_MAX_PX,
    -SCROLL_ROUNDING_MAX_PX,
  ])('removes the shift %s, at SCROLL_ROUNDING_MAX_PX', (shift) => {
    const { el, state, writeLayout } = fakeScroller({ scrollTop: 1250, shift })

    expect(applyRebaseWrite(el, shift, writeLayout)).toBe(0)

    expect(state.scrollTop).toBe(1250 - shift)
    expect(writeLayout).toHaveBeenLastCalledWith(0)
  })

  it('writes the position before the layout for a positive shift', () => {
    // The spacer is 2380 + 200 px, so the position is at the maximum.
    const { el, state, writes, writeLayout, contentTop } = fakeScroller({ scrollTop: 2080, shift: 200 })
    const before = contentTop()

    expect(applyRebaseWrite(el, 200, writeLayout)).toBe(0)

    expect(writes).toEqual(['scrollTop', 'layout'])
    expect(writeLayout).toHaveBeenCalledExactlyOnceWith(0)
    expect(state.scrollTop).toBe(1880)
    expect(contentTop()).toBe(before)
  })

  it('writes the layout before the position for a negative shift, so the range holds the target', () => {
    // The spacer is 2380 - 200 px, so the position is at the maximum, and the target 1880
    // lies below it until the spacer grows.
    const { el, state, writes, writeLayout, contentTop } = fakeScroller({ scrollTop: 1680, shift: -200 })
    const before = contentTop()

    expect(applyRebaseWrite(el, -200, writeLayout)).toBe(0)

    expect(writes).toEqual(['layout', 'scrollTop'])
    // A remainder of 0 keeps the layout that the first write left.
    expect(writeLayout).toHaveBeenCalledExactlyOnceWith(0)
    expect(state.scrollTop).toBe(1880)
    expect(contentTop()).toBe(before)
  })

  it.each([
    {
      name: 'a positive',
      scrollTop: 2000,
      shift: 200.4,
      writes: ['scrollTop', 'layout'],
      kept: 0.4,
    },
    {
      name: 'a negative',
      scrollTop: 1250,
      shift: -60.37,
      writes: ['layout', 'scrollTop', 'layout'],
      kept: -0.37,
    },
  ])('keeps the rounding remainder of $name shift as the shift, so the content stays in place', (c) => {
    const { el, state, writes, writeLayout, contentTop } = fakeScroller({
      scrollTop: c.scrollTop,
      shift: c.shift,
      round: Math.round,
    })
    const before = contentTop()

    const kept = applyRebaseWrite(el, c.shift, writeLayout)

    expect(kept).toBeCloseTo(c.kept, 9)
    expect(Number.isInteger(state.scrollTop)).toBe(true)
    expect(writes).toEqual(c.writes)
    expect(writeLayout).toHaveBeenLastCalledWith(kept)
    expect(contentTop()).toBeCloseTo(before, 9)
  })

  it.each([
    // The target -100 lies above the scroll range, so the clamp moves it by 100px.
    { name: 'far above the range', scrollTop: 100 },
    // The target -1 lies one pixel above the scroll range.
    { name: 'SCROLL_ROUNDING_MAX_PX above the range', scrollTop: 200 - SCROLL_ROUNDING_MAX_PX },
  ])('drops the remainder of a clamp of a positive shift $name', ({ scrollTop }) => {
    const { el, state, writes, writeLayout } = fakeScroller({ scrollTop, shift: 200 })

    expect(applyRebaseWrite(el, 200, writeLayout)).toBe(0)

    expect(state.scrollTop).toBe(0)
    expect(writes).toEqual(['scrollTop', 'layout'])
    expect(writeLayout).toHaveBeenCalledExactlyOnceWith(0)
  })

  it('keeps a remainder below SCROLL_ROUNDING_MAX_PX of a clamp of a positive shift', () => {
    // The target -0.5 lies inside the blank band above the first row, half a pixel above
    // the scroll range. The shift keeps that half pixel, so the content stays in place.
    const { el, state, writeLayout, contentTop } = fakeScroller({ scrollTop: 199.5, shift: 200 })
    const before = contentTop()

    expect(applyRebaseWrite(el, 200, writeLayout)).toBe(0.5)

    expect(state.scrollTop).toBe(0)
    expect(writeLayout).toHaveBeenLastCalledWith(0.5)
    expect(contentTop()).toBe(before)
  })

  it('drops the remainder of a clamp of a negative shift, and writes the layout once', () => {
    // No spacer follows the layout, so the scroll range stays 1680, below the target 1880.
    const { el, state, writes, writeLayout } = fakeScroller({ scrollTop: 1680, shift: -200, staticRange: true })

    expect(applyRebaseWrite(el, -200, writeLayout)).toBe(0)

    expect(state.scrollTop).toBe(1680)
    expect(writes).toEqual(['layout', 'scrollTop'])
    expect(writeLayout).toHaveBeenCalledExactlyOnceWith(0)
  })
})

describe('placementScrollTop', () => {
  /** A conversion with an inset of 28px and no shift. */
  const insetOf28 = () => vi.fn((contentTop: number) => contentTop + 28)

  it.each([
    0,
    -0,
    -8,
    -1e9,
  ])('places the content coordinate %s, at or above the content top, at the top of the scroller', (contentTop) => {
    const scrollTopOf = insetOf28()

    expect(placementScrollTop(contentTop, scrollTopOf)).toBe(0)

    expect(scrollTopOf).not.toHaveBeenCalled()
  })

  it.each([
    [0.01, 28.01],
    [12, 40],
    [1e9, 1e9 + 28],
  ])('converts the content coordinate %s, below the content top, to %s', (contentTop, expected) => {
    const scrollTopOf = insetOf28()

    expect(placementScrollTop(contentTop, scrollTopOf)).toBeCloseTo(expected, 9)

    expect(scrollTopOf).toHaveBeenCalledExactlyOnceWith(contentTop)
  })
})
