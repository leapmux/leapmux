import { describe, expect, it } from 'vitest'
import { FOLLOW_TOLERANCE_PX } from './chatScrollGeometry'
import { createSeenScrollPosition } from './chatScrollSeenPosition'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

/**
 * A list of `scrollHeight` px in a 500px view at `scrollTop`, and a seen position that noted
 * it, as the scroll handler or an own write does. By default: 5000px at its bottom, 4500.
 */
function notedAt(scrollTop = 4500, scrollHeight = 5000) {
  const div = makeFakeScrollDiv()
  div.setClientHeight(500)
  div.setScrollHeight(scrollHeight)
  div.setScrollTop(scrollTop)
  const seen = createSeenScrollPosition()
  seen.note(div.el)
  return { div, seen }
}

/** A list at its bottom, 4500, that the seen position noted (see notedAt). */
const atBottom = () => notedAt()

describe('createSeenScrollPosition', () => {
  it('starts at 0', () => {
    const seen = createSeenScrollPosition()
    expect(seen.seenTop()).toBe(0)
  })

  it('finds no upward move before the first note', () => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(500)
    div.setScrollHeight(5000)
    const seen = createSeenScrollPosition()
    expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    expect(seen.lowestMaxTop(div.el)).toBe(0)
  })

  it('returns the clamped position of the last note', () => {
    const { div, seen } = atBottom()
    // A rubber-band overscroll at the top: the raw position is below 0.
    div.setRawScrollTop(-20)
    seen.note(div.el)
    expect(seen.seenTop()).toBe(0)
    div.setScrollTop(1200)
    seen.note(div.el)
    expect(seen.seenTop()).toBe(1200)
  })

  it('clamps the seen position of an overscroll at the bottom to the maximum', () => {
    const { div, seen } = atBottom()
    div.setRawScrollTop(4530)
    seen.note(div.el)
    expect(seen.seenTop()).toBe(4500)
  })

  describe('hasUnseenRawMove', () => {
    it('finds no move at the noted position', () => {
      const { div, seen } = atBottom()
      expect(seen.hasUnseenRawMove(div.el)).toBe(false)
    })

    it('finds a move with no event in either direction', () => {
      const { div, seen } = atBottom()
      div.setScrollTop(4499)
      expect(seen.hasUnseenRawMove(div.el)).toBe(true)
      div.setScrollTop(4000)
      seen.note(div.el)
      div.setScrollTop(4100)
      expect(seen.hasUnseenRawMove(div.el)).toBe(true)
    })

    it('compares the raw position, not the clamped one', () => {
      const { div, seen } = atBottom()
      // A rubber-band overscroll at the top: the clamped position stays 0 while the raw
      // position moves.
      div.setRawScrollTop(-20)
      seen.note(div.el)
      expect(seen.hasUnseenRawMove(div.el)).toBe(false)
      div.setRawScrollTop(0)
      expect(seen.hasUnseenRawMove(div.el)).toBe(true)
    })

    it('finds the clamp of a taller viewport as a raw move', () => {
      const { div, seen } = atBottom()
      div.setClientHeight(600)
      expect(div.getScrollTop()).toBe(4400)
      expect(seen.hasUnseenRawMove(div.el)).toBe(true)
    })
  })

  describe('lowestMaxTop', () => {
    it('is the maximum position of the note while nothing changes', () => {
      const { div, seen } = notedAt(4000)
      expect(seen.lowestMaxTop(div.el)).toBe(4500)
    })

    it('follows the current maximum down, and not up, with no record', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      expect(seen.lowestMaxTop(div.el)).toBe(4400)
      div.setScrollHeight(5200)
      expect(seen.lowestMaxTop(div.el)).toBe(4500)
    })

    it('keeps the lowest maximum that noteRange recorded', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      seen.noteRange(div.el)
      div.setScrollHeight(4950)
      seen.noteRange(div.el)
      div.setScrollHeight(5200)
      expect(seen.lowestMaxTop(div.el)).toBe(4400)
    })

    it('lowers the maximum of the note by the growth of the viewport, also after the content grew again', () => {
      const { div, seen } = atBottom()
      // The viewport grows by 100px: the maximum is 4400. The content then grows by 40px.
      div.setClientHeight(600)
      div.setScrollHeight(5040)
      expect(div.el.scrollHeight - div.el.clientHeight).toBe(4440)
      expect(seen.lowestMaxTop(div.el)).toBe(4400)
    })

    it('ignores a viewport that shrinks', () => {
      const { div, seen } = atBottom()
      div.setClientHeight(400)
      expect(seen.lowestMaxTop(div.el)).toBe(4500)
    })

    it('measures the growth of the viewport from the last note', () => {
      const { div, seen } = atBottom()
      div.setClientHeight(600)
      seen.note(div.el)
      expect(seen.lowestMaxTop(div.el)).toBe(4400)
      div.setScrollHeight(5040)
      expect(seen.lowestMaxTop(div.el)).toBe(4400)
      div.setClientHeight(650)
      expect(seen.lowestMaxTop(div.el)).toBe(4350)
    })

    it('never goes below 0', () => {
      // A list of 600px in a 500px view, at its bottom, 100. The viewport grows by 400px.
      const { div, seen } = notedAt(100, 600)
      div.setClientHeight(900)
      div.setScrollHeight(2000)
      expect(seen.lowestMaxTop(div.el)).toBe(0)
    })

    it('drops the recorded range at the next note', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      seen.noteRange(div.el)
      div.setScrollHeight(5200)
      seen.note(div.el)
      expect(seen.lowestMaxTop(div.el)).toBe(4700)
    })

    it('is 0 for a list that is shorter than the view, whatever the viewport does', () => {
      // 300px of content in a 500px view: the list cannot scroll.
      const { div, seen } = notedAt(0, 300)
      expect(seen.seenTop()).toBe(0)
      expect(seen.lowestMaxTop(div.el)).toBe(0)
      div.setClientHeight(800)
      expect(seen.lowestMaxTop(div.el)).toBe(0)
      div.setClientHeight(200)
      expect(seen.lowestMaxTop(div.el)).toBe(0)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })
  })

  describe('hasUnseenUpwardMove', () => {
    it('finds no move at the noted position', () => {
      const { div, seen } = atBottom()
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('finds a move up past the follow tolerance with no event', () => {
      const { div, seen } = atBottom()
      div.setScrollTop(4500 - FOLLOW_TOLERANCE_PX - 0.5)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
      div.setScrollTop(1000)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
    })

    it('finds no move of at most the follow tolerance', () => {
      const { div, seen } = atBottom()
      div.setScrollTop(4500 - FOLLOW_TOLERANCE_PX)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('finds no upward move for a move down', () => {
      const { div, seen } = notedAt(4000)
      div.setScrollTop(4200)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('finds no move after the event of the move noted it', () => {
      const { div, seen } = atBottom()
      div.setScrollTop(4000)
      seen.note(div.el)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('reads a clamp that a shrink forced as no move, with no record', () => {
      const { div, seen } = atBottom()
      // The content shrinks by 100px: the browser clamps the view to the new maximum.
      div.setScrollHeight(4900)
      expect(div.getScrollTop()).toBe(4400)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('reads a clamp that no check recorded as a move once the content grows again', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      // The predicate records nothing, so the growth hides the clamp.
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
      div.setScrollHeight(5200)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
    })

    it('keeps a clamp that noteRange recorded forced after the content grows again', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      seen.noteRange(div.el)
      div.setScrollHeight(5200)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
      // A move of the reader above the clamped position still counts.
      div.setScrollTop(4400 - FOLLOW_TOLERANCE_PX - 1)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
    })

    it('keeps the lowest maximum that noteRange recorded', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      seen.noteRange(div.el)
      div.setScrollHeight(5200)
      seen.noteRange(div.el)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('records no clamp in noteRange for a range that holds the position', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(5200)
      seen.noteRange(div.el)
      div.setScrollTop(4300)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
    })

    it('drops the recorded clamp at the next note', () => {
      const { div, seen } = atBottom()
      div.setScrollHeight(4900)
      seen.noteRange(div.el)
      div.setScrollHeight(5200)
      div.setScrollTop(4700)
      seen.note(div.el)
      div.setScrollTop(4600)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
    })

    describe('a viewport that grows taller', () => {
      it('reads the clamp as no move', () => {
        const { div, seen } = atBottom()
        div.setClientHeight(600)
        expect(div.getScrollTop()).toBe(4400)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
      })

      it('reads the clamp as no move after the content grows again, with no record', () => {
        const { div, seen } = atBottom()
        // A send shrinks the composer, so the list grows by 100px and the browser clamps the
        // view to 4400. The thinking indicator then adds 40px, and no check ran in between.
        div.setClientHeight(600)
        div.setScrollHeight(5040)
        expect(div.getScrollTop()).toBe(4400)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
      })

      it('finds a move of the reader beyond the growth of the viewport', () => {
        const { div, seen } = atBottom()
        div.setClientHeight(600)
        div.setScrollHeight(5040)
        div.setScrollTop(4400 - FOLLOW_TOLERANCE_PX)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
        div.setScrollTop(4400 - FOLLOW_TOLERANCE_PX - 0.5)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
      })

      it('finds a move of a reader far above the bottom, whom the growth cannot clamp', () => {
        // The reader is at 3000, 1500px above the bottom. A viewport that grows by 100px
        // lowers the maximum to 4400, which is still far below the reader.
        const { div, seen } = notedAt(3000)
        div.setClientHeight(600)
        expect(div.getScrollTop()).toBe(3000)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
        div.setScrollTop(3000 - FOLLOW_TOLERANCE_PX - 0.5)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
      })

      it('accounts only for the part of the growth that reaches a reader near the bottom', () => {
        // The reader is at 4450, 50px above the bottom. A viewport that grows by 100px lowers
        // the maximum to 4400, so the clamp moves the reader by 50px, and no farther.
        const { div, seen } = notedAt(4450)
        div.setClientHeight(600)
        div.setScrollHeight(5040)
        expect(div.getScrollTop()).toBe(4400)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
        div.setScrollTop(4400 - FOLLOW_TOLERANCE_PX - 0.5)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
      })

      it('finds a move up under a viewport that shrinks', () => {
        const { div, seen } = atBottom()
        div.setClientHeight(400)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
        div.setScrollTop(4490)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
      })

      it('finds a bounce back from an overscroll at the bottom as no move', () => {
        const { div, seen } = atBottom()
        div.setRawScrollTop(4530)
        seen.note(div.el)
        div.setScrollHeight(5200)
        div.setScrollTop(4500)
        expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
      })
    })
  })
})
