import { describe, expect, it } from 'vitest'
import { createStaleNativeScrollTranslator } from './chatScrollStaleNative'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

describe('chatscrollstalenative translator', () => {
  function setup(opts: { inputActive?: boolean, echo?: boolean } = {}) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(50000)
    div.setClientHeight(500)
    let now = 1000
    const baselines: number[] = []
    const translator = createStaleNativeScrollTranslator({
      getEl: () => div.el,
      isScrollInputActive: () => opts.inputActive ?? false,
      isProgrammaticEcho: () => opts.echo ?? false,
      setLastScrollTopForDir: top => baselines.push(top),
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
