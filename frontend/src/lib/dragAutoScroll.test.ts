import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { EDGE_BAND_PX, edgeScrollStep, MAX_SCROLL_STEP_PX, scrollableAncestor, startEdgeScroll } from '~/lib/dragAutoScroll'

const VIEWPORT = { top: 100, bottom: 500 }

describe('edgeScrollStep', () => {
  it('stands still while the pointer is away from both edges', () => {
    expect(edgeScrollStep(VIEWPORT, 300)).toBe(0)
  })

  it('stands still exactly on each band boundary', () => {
    // The band is EXCLUSIVE at its inner edge, so a pointer resting there does
    // not creep. Without this the list drifts under a stationary finger.
    expect(edgeScrollStep(VIEWPORT, VIEWPORT.top + EDGE_BAND_PX)).toBe(0)
    expect(edgeScrollStep(VIEWPORT, VIEWPORT.bottom - EDGE_BAND_PX)).toBe(0)
  })

  it('scrolls up inside the top band and down inside the bottom band', () => {
    expect(edgeScrollStep(VIEWPORT, VIEWPORT.top + 1)).toBeLessThan(0)
    expect(edgeScrollStep(VIEWPORT, VIEWPORT.bottom - 1)).toBeGreaterThan(0)
  })

  it('ramps with the depth into the band, so a short list stays controllable', () => {
    const shallow = edgeScrollStep(VIEWPORT, VIEWPORT.bottom - EDGE_BAND_PX + 1)
    const deep = edgeScrollStep(VIEWPORT, VIEWPORT.bottom - 1)
    expect(deep).toBeGreaterThan(shallow)
    expect(shallow).toBeGreaterThan(0)
  })

  it('caps at the maximum step past the edge, however far the pointer goes', () => {
    expect(edgeScrollStep(VIEWPORT, VIEWPORT.bottom + 1000)).toBe(MAX_SCROLL_STEP_PX)
    expect(edgeScrollStep(VIEWPORT, VIEWPORT.top - 1000)).toBe(-MAX_SCROLL_STEP_PX)
  })

  it('stands still for a viewport shorter than two bands', () => {
    // The two bands would overlap, so a pointer in the middle would belong to
    // both. A box that small already shows every row.
    const tiny = { top: 0, bottom: EDGE_BAND_PX }
    expect(edgeScrollStep(tiny, 1)).toBe(0)
    expect(edgeScrollStep(tiny, EDGE_BAND_PX - 1)).toBe(0)
  })
})

describe('scrollableAncestor', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  function box(overflowY: string, scrollHeight: number, clientHeight: number): HTMLElement {
    const el = document.createElement('div')
    el.style.overflowY = overflowY
    Object.defineProperty(el, 'scrollHeight', { get: () => scrollHeight, configurable: true })
    Object.defineProperty(el, 'clientHeight', { get: () => clientHeight, configurable: true })
    return el
  }

  it('finds the nearest ancestor that actually overflows', () => {
    const outer = box('auto', 900, 300)
    const inner = box('visible', 100, 100)
    const leaf = document.createElement('span')
    inner.appendChild(leaf)
    outer.appendChild(inner)
    document.body.appendChild(outer)
    expect(scrollableAncestor(leaf)).toBe(outer)
  })

  it('skips a scroll container whose content already fits', () => {
    // `overflow-y: auto` alone is not enough. Half the app's panels declare it
    // and never overflow, and treating one as the scroller would auto-scroll a
    // box that cannot move.
    const outer = box('auto', 900, 300)
    const inner = box('auto', 100, 100)
    const leaf = document.createElement('span')
    inner.appendChild(leaf)
    outer.appendChild(inner)
    document.body.appendChild(outer)
    expect(scrollableAncestor(leaf)).toBe(outer)
  })

  it('returns undefined when nothing above the element scrolls', () => {
    const leaf = document.createElement('span')
    document.body.appendChild(leaf)
    expect(scrollableAncestor(leaf)).toBeUndefined()
  })

  it('returns undefined for a detached node', () => {
    expect(scrollableAncestor(null)).toBeUndefined()
  })
})

describe('startEdgeScroll', () => {
  let frames: Array<() => void>

  beforeEach(() => {
    frames = []
    vi.stubGlobal('requestAnimationFrame', (cb: () => void) => {
      frames.push(cb)
      return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', (id: number) => {
      frames[id - 1] = () => {}
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  /** Run every frame queued so far, once. */
  function tick() {
    const pending = frames.splice(0, frames.length)
    for (const frame of pending)
      frame()
  }

  function scroller(opts: { scrollHeight: number, clientHeight: number }): HTMLElement {
    const el = document.createElement('div')
    let top = 0
    Object.defineProperty(el, 'scrollTop', {
      get: () => top,
      set: (next: number) => { top = Math.max(0, Math.min(next, opts.scrollHeight - opts.clientHeight)) },
      configurable: true,
    })
    el.getBoundingClientRect = () => ({ top: 0, bottom: opts.clientHeight, left: 0, right: 100, width: 100, height: opts.clientHeight, x: 0, y: 0, toJSON: () => ({}) })
    return el
  }

  it('scrolls and reports the pixels it applied while the pointer sits at an edge', () => {
    const el = scroller({ scrollHeight: 1000, clientHeight: 400 })
    const applied: number[] = []
    const loop = startEdgeScroll(el, px => applied.push(px))

    loop.track(399)
    tick()

    expect(el.scrollTop).toBeGreaterThan(0)
    expect(applied).toEqual([el.scrollTop])
    loop.stop()
  })

  it('keeps running frame after frame until the pointer leaves the band', () => {
    const el = scroller({ scrollHeight: 1000, clientHeight: 400 })
    const loop = startEdgeScroll(el, () => {})

    loop.track(399)
    tick()
    const afterFirst = el.scrollTop
    tick()
    expect(el.scrollTop).toBeGreaterThan(afterFirst)

    loop.track(200)
    tick()
    const settled = el.scrollTop
    tick()
    expect(el.scrollTop).toBe(settled)
    loop.stop()
  })

  it('reports nothing and stops once the scroller reaches its end', () => {
    // The APPLIED delta, not the wanted one. A box already at the bottom moves
    // zero, and reporting the wanted amount would push the drag's compensation
    // past what the container actually did.
    const el = scroller({ scrollHeight: 410, clientHeight: 400 })
    const applied: number[] = []
    const loop = startEdgeScroll(el, px => applied.push(px))

    loop.track(399)
    tick()
    expect(el.scrollTop).toBe(10)
    expect(applied).toEqual([10])

    tick()
    expect(applied).toEqual([10])
    loop.stop()
  })

  it('stops scrolling after stop(), even with a frame already queued', () => {
    const el = scroller({ scrollHeight: 1000, clientHeight: 400 })
    const loop = startEdgeScroll(el, () => {})
    loop.track(399)
    loop.stop()
    tick()
    expect(el.scrollTop).toBe(0)
  })
})
