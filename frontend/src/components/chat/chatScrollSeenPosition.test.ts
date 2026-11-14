import { describe, expect, it } from 'vitest'
import { FOLLOW_TOLERANCE_PX } from './chatScrollGeometry'
import { createSeenScrollPosition } from './chatScrollSeenPosition'
import { makeFakeScrollDiv } from './useChatScroll.testkit'

/**
 * A list of 5000px in a 500px view at its bottom, 4500, and a seen position that noted
 * that bottom, as the scroll handler or an own write does.
 */
function atBottom() {
  const div = makeFakeScrollDiv()
  div.setClientHeight(500)
  div.setScrollHeight(5000)
  div.setScrollTop(4500)
  const seen = createSeenScrollPosition()
  seen.note(4500, 4500)
  return { div, seen }
}

describe('createSeenScrollPosition', () => {
  it('starts at 0', () => {
    const seen = createSeenScrollPosition()
    expect(seen.seenTop()).toBe(0)
  })

  it('returns the clamped position of the last note', () => {
    const seen = createSeenScrollPosition()
    seen.note(0, -20)
    expect(seen.seenTop()).toBe(0)
    seen.note(1200, 1200)
    expect(seen.seenTop()).toBe(1200)
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
      seen.note(4000, 4000)
      div.setScrollTop(4100)
      expect(seen.hasUnseenRawMove(div.el)).toBe(true)
    })

    it('compares the raw position, not the clamped one', () => {
      const { div, seen } = atBottom()
      // A rubber-band overscroll at the top: the clamped position stays 0 while the raw
      // position moves.
      seen.note(0, -20)
      div.setRawScrollTop(-20)
      expect(seen.hasUnseenRawMove(div.el)).toBe(false)
      div.setRawScrollTop(0)
      expect(seen.hasUnseenRawMove(div.el)).toBe(true)
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
      const { div, seen } = atBottom()
      seen.note(4000, 4000)
      div.setScrollTop(4200)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(false)
    })

    it('finds no move after the event of the move noted it', () => {
      const { div, seen } = atBottom()
      div.setScrollTop(4000)
      seen.note(4000, 4000)
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
      seen.note(4700, 4700)
      div.setScrollTop(4600)
      expect(seen.hasUnseenUpwardMove(div.el)).toBe(true)
    })
  })
})
