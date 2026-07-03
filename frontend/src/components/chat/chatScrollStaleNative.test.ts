import { describe, expect, it } from 'vitest'
import { createStaleNativeScrollTranslator } from './chatScrollStaleNative'
import { flingOverscanCapPx } from './useChatScroll'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

describe('chatscrollstalenative translator', () => {
  function setup(opts: { inputActive?: boolean, echo?: boolean, clientHeight?: number } = {}) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(50000)
    div.setClientHeight(opts.clientHeight ?? 500)
    let now = 1000
    const baselines: number[] = []
    const translator = createStaleNativeScrollTranslator({
      getEl: () => div.el,
      isScrollInputActive: () => opts.inputActive ?? false,
      isProgrammaticEcho: () => opts.echo ?? false,
      setLastScrollTopForDir: top => baselines.push(top),
      // The real pane-derived cap, so the old-coordinate window couples to the fling
      // render-ahead exactly as production does.
      flingOverscanCapPx,
      now: () => now,
    })
    const advance = (ms: number) => {
      now += ms
    }
    return { div, translator, baselines, advance }
  }

  /** Arm a screen-plus repin shift: beforeTop 1000 -> afterTop 4000 (delta +3000). */
  function armPrependShift(t: ReturnType<typeof setup>) {
    t.translator.noteProgrammaticWrite({
      source: 'anchor-repin',
      beforeTop: 1000,
      afterTop: 4000,
      clientHeight: 500,
      dir: 'older',
    })
  }

  it('translates a delayed old-coordinate momentum event by the repin delta', () => {
    const t = setup()
    armPrependShift(t)
    // Compositor-delayed momentum lands near the OLD coordinate, continuing the
    // user's upward (older) intent past the pre-repin position.
    t.div.setScrollTop(900)
    t.advance(50)
    expect(t.translator.translate()).toBe(true)
    expect(t.div.getScrollTop()).toBe(3900) // 900 + 3000, in the current space
    expect(t.baselines).toEqual([4000]) // baseline advanced to the repin's landing
  })

  it('does not translate a direct drag or our own echo (already current-coordinate)', () => {
    const dragging = setup({ inputActive: true })
    armPrependShift(dragging)
    dragging.div.setScrollTop(900)
    expect(dragging.translator.translate()).toBe(false)

    const echoing = setup({ echo: true })
    armPrependShift(echoing)
    echoing.div.setScrollTop(900)
    expect(echoing.translator.translate()).toBe(false)
  })

  it('expires the shift after the translate window', () => {
    const t = setup()
    armPrependShift(t)
    t.div.setScrollTop(900)
    t.advance(301)
    expect(t.translator.translate()).toBe(false)
    // And the record is gone: an in-window retry no longer translates either.
    t.advance(-300)
    expect(t.translator.translate()).toBe(false)
  })

  it('disarms once a genuine current-coordinate scroll arrives (no later mistranslation)', () => {
    const t = setup()
    armPrependShift(t)
    // A normal post-repin scroll well away from the old coordinate: not translated,
    // and the shift is dropped so a LATER scroll that merely crosses the old midpoint
    // cannot be yanked by the obsolete delta.
    t.div.setScrollTop(3800)
    expect(t.translator.translate()).toBe(false)
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBe(false)
  })

  it('disarms when a compensating second repin brings the cumulative shift back under a screen', () => {
    const t = setup()
    armPrependShift(t)
    // A second repin (e.g. a far-above trim) moves 4000 -> 1200: cumulative movement
    // from the original 1000 is now 200px -- native momentum is already current-space.
    t.translator.noteProgrammaticWrite({
      source: 'anchor-repin',
      beforeTop: 4000,
      afterTop: 1200,
      clientHeight: 500,
      dir: 'older',
    })
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBe(false)
  })

  it('covers the full pane-derived fling render-ahead on a tall pane', () => {
    // Regression: the old-coordinate window was a frozen max(clientHeight*2, 1800). Once
    // the fling render-ahead cap became pane-derived (up to 2.5 screens), that window
    // under-reached it on a tall pane -- a stale momentum event that coasted between two
    // screens and the render-ahead cap failed classification and jumped. The window now
    // tracks flingOverscanCapPx, so it covers the whole travel.
    const clientHeight = 1000
    const t = setup({ clientHeight })
    // flingOverscanCapPx(1000) = 2500; the old window would have been max(2000, 1800) = 2000.
    expect(flingOverscanCapPx(clientHeight)).toBe(2500)
    t.translator.noteProgrammaticWrite({
      source: 'anchor-repin',
      beforeTop: 3000,
      afterTop: 8000, // delta +5000, a full-screen-plus prepend repin
      clientHeight,
      dir: 'older',
    })
    // A compositor-delayed momentum event 2200px above the pre-repin position: within the
    // 2500px render-ahead but BEYOND the old 2000px (clientHeight*2) window.
    t.div.setScrollTop(800)
    t.advance(50)
    expect(t.translator.translate()).toBe(true)
    expect(t.div.getScrollTop()).toBe(5800) // 800 + 5000 delta, into the current space
  })

  it('invalidates the shift on any other programmatic write source', () => {
    const t = setup()
    armPrependShift(t)
    t.translator.noteProgrammaticWrite({
      source: 'stick-bottom',
      beforeTop: 4000,
      afterTop: 49500,
      clientHeight: 500,
      dir: 'newer',
    })
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBe(false)
  })
})
