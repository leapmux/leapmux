import { describe, expect, it } from 'vitest'
import { createStaleNativeScrollTranslator } from './chatScrollStaleNative'
import { flingOverscanCapPx } from './useChatScroll'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

describe('chatScrollStaleNative translator', () => {
  function setup(opts: { inputActive?: boolean, ownWrite?: boolean, clientHeight?: number } = {}) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(50000)
    div.setClientHeight(opts.clientHeight ?? 500)
    let now = 1000
    const translator = createStaleNativeScrollTranslator({
      getEl: () => div.el,
      isScrollInputActive: () => opts.inputActive ?? false,
      isOwnWriteEvent: () => opts.ownWrite ?? false,
      // The real pane-derived cap, so the old-coordinate window couples to the fling
      // render-ahead exactly as production does.
      flingOverscanCapPx,
      now: () => now,
    })
    const advance = (ms: number) => {
      now += ms
    }
    return { div, translator, advance }
  }

  /** Arm a screen-plus rebase shift: beforeTop 1000 -> afterTop 4000 (delta +3000). */
  function armPrependShift(t: ReturnType<typeof setup>) {
    t.translator.noteRebase({
      beforeTop: 1000,
      afterTop: 4000,
      clientHeight: 500,
      dir: 'older',
    })
  }

  it('translates a delayed old-coordinate momentum event by the rebase delta', () => {
    const t = setup()
    armPrependShift(t)
    // Compositor-delayed momentum lands near the OLD coordinate, continuing the
    // user's upward (older) intent past the pre-rebase position.
    t.div.setScrollTop(900)
    t.advance(50)
    // The baseline of the move: the position where the rebase landed.
    expect(t.translator.translate()).toBe(4000)
    expect(t.div.getScrollTop()).toBe(3900) // 900 + 3000, in the current space
  })

  it('translates an old-coordinate event that lands slightly on the newer side', () => {
    // The rebase moved the view from 1000 to 4000 (delta +3000) while the reader scrolled
    // toward older rows. Event coalescing or a bounce-back can deliver an old-coordinate
    // event a little past 1000 on the newer side, against the direction of the scroll. Such
    // an event still counts as old-coordinate when its distance from 1000 is at most
    // min(500 * 0.4, 3000 * 0.15) = min(200, 450) = 200.
    const afterTranslate = (staleTop: number) => {
      const t = setup()
      armPrependShift(t)
      t.div.setScrollTop(staleTop)
      t.advance(50)
      const baseline = t.translator.translate()
      return {
        baseline,
        scrollTop: t.div.getScrollTop(),
      }
    }
    expect(afterTranslate(1150)).toEqual({ baseline: 4000, scrollTop: 4150 })
    expect(afterTranslate(1200)).toEqual({ baseline: 4000, scrollTop: 4200 })
    // 250px is past the 200px limit, although it is under the 450px of the delta ratio.
    expect(afterTranslate(1250)).toEqual({ baseline: undefined, scrollTop: 1250 })
  })

  it('does not translate a direct drag or the event of an own write (already current-coordinate)', () => {
    const dragging = setup({ inputActive: true })
    armPrependShift(dragging)
    dragging.div.setScrollTop(900)
    expect(dragging.translator.translate()).toBeUndefined()

    const ownWrite = setup({ ownWrite: true })
    armPrependShift(ownWrite)
    ownWrite.div.setScrollTop(900)
    expect(ownWrite.translator.translate()).toBeUndefined()
  })

  it('expires the shift after the translate window', () => {
    const t = setup()
    armPrependShift(t)
    t.div.setScrollTop(900)
    t.advance(301)
    expect(t.translator.translate()).toBeUndefined()
    // And the record is gone: an in-window retry no longer translates either.
    t.advance(-300)
    expect(t.translator.translate()).toBeUndefined()
  })

  it('keeps translating an old-coordinate stream past the initial window', () => {
    const t = setup({ clientHeight: 596 })
    t.translator.noteRebase({
      beforeTop: 262,
      afterTop: 1646,
      clientHeight: 596,
      dir: 'older',
    })

    // The iOS capture still reported positions in the old coordinates 308ms after the rebase.
    t.advance(275)
    t.div.setScrollTop(82)
    expect(t.translator.translate()).toBe(1646)
    expect(t.div.getScrollTop()).toBe(1466)

    t.advance(33)
    t.div.setScrollTop(66)
    expect(t.translator.translate()).toBe(1646)
    expect(t.div.getScrollTop()).toBe(1450)

    t.advance(16)
    t.div.setScrollTop(1410)
    expect(t.translator.translate()).toBeUndefined()
    t.div.setScrollTop(50)
    expect(t.translator.translate()).toBeUndefined()
    expect(t.div.getScrollTop()).toBe(50)
  })

  it('expires after a quiet interval even when an own write echo arrived', () => {
    const opts = { ownWrite: false }
    const t = setup(opts)
    armPrependShift(t)
    t.advance(275)
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBe(4000)

    t.advance(200)
    opts.ownWrite = true
    expect(t.translator.translate()).toBeUndefined()

    t.advance(101)
    opts.ownWrite = false
    t.div.setScrollTop(800)
    expect(t.translator.translate()).toBeUndefined()
    expect(t.div.getScrollTop()).toBe(800)
  })

  it('combines a second rebase with an old-coordinate stream that is still active', () => {
    const t = setup()
    armPrependShift(t)
    t.advance(275)
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBe(4000)
    expect(t.div.getScrollTop()).toBe(3900)

    t.advance(33)
    t.translator.noteRebase({
      beforeTop: 3900,
      afterTop: 5000,
      clientHeight: 500,
      dir: 'older',
    })
    t.div.setScrollTop(850)
    expect(t.translator.translate()).toBe(5000)
    expect(t.div.getScrollTop()).toBe(4850)
  })

  it('disarms once a genuine current-coordinate scroll arrives (no later mistranslation)', () => {
    const t = setup()
    armPrependShift(t)
    // A normal post-rebase scroll well away from the old coordinate: not translated,
    // and the shift is dropped so a LATER scroll that merely crosses the old midpoint
    // cannot be yanked by the obsolete delta.
    t.div.setScrollTop(3800)
    expect(t.translator.translate()).toBeUndefined()
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBeUndefined()
  })

  it('disarms when a compensating second rebase brings the cumulative shift back under a screen', () => {
    const t = setup()
    armPrependShift(t)
    // A second rebase (e.g. a far-above trim) moves 4000 -> 1200: cumulative movement
    // from the original 1000 is now 200px -- native momentum is already current-space.
    t.translator.noteRebase({
      beforeTop: 4000,
      afterTop: 1200,
      clientHeight: 500,
      dir: 'older',
    })
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBeUndefined()
  })

  it('keeps the shift through a rebase of a hidden pane', () => {
    const t = setup()
    armPrependShift(t)
    // With a shown pane, this rebase would compensate the first one and disarm the shift.
    t.translator.noteRebase({
      beforeTop: 4000,
      afterTop: 1200,
      clientHeight: 0,
      dir: 'older',
    })
    t.div.setScrollTop(900)
    t.advance(50)
    expect(t.translator.translate()).toBe(4000)
    expect(t.div.getScrollTop()).toBe(3900)
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
    t.translator.noteRebase({
      beforeTop: 3000,
      afterTop: 8000, // delta +5000, a full-screen-plus prepend rebase
      clientHeight,
      dir: 'older',
    })
    // A compositor-delayed momentum event 2200px above the pre-rebase position: within the
    // 2500px render-ahead but BEYOND the old 2000px (clientHeight*2) window.
    t.div.setScrollTop(800)
    t.advance(50)
    expect(t.translator.translate()).toBe(8000)
    expect(t.div.getScrollTop()).toBe(5800) // 800 + 5000 delta, into the current space
  })

  it('forgets the shift when an own write invalidates it', () => {
    const t = setup()
    armPrependShift(t)
    t.translator.invalidate()
    t.div.setScrollTop(900)
    expect(t.translator.translate()).toBeUndefined()
  })
})
