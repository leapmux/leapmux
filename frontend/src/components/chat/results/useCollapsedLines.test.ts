import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { COLLAPSED_LINE_CHAR_CAP, useCollapsedLines } from './useCollapsedLines'

describe('usecollapsedlines', () => {
  it('keeps the body uncollapsed when the line count is at or below the threshold', () => {
    createRoot((dispose) => {
      const { display, isCollapsed } = useCollapsedLines({
        text: () => 'a\nb\nc',
        expanded: () => false,
        threshold: 3,
      })
      expect(isCollapsed()).toBe(false)
      expect(display()).toBe('a\nb\nc')
      dispose()
    })
  })

  it('collapses to the threshold when the body exceeds it and not expanded', () => {
    createRoot((dispose) => {
      const { display, isCollapsed } = useCollapsedLines({
        text: () => 'a\nb\nc\nd\ne',
        expanded: () => false,
        threshold: 3,
      })
      expect(isCollapsed()).toBe(true)
      expect(display()).toBe('a\nb\nc')
      dispose()
    })
  })

  it('shows the full body when expanded, regardless of length', () => {
    createRoot((dispose) => {
      const { display, isCollapsed } = useCollapsedLines({
        text: () => 'a\nb\nc\nd\ne',
        expanded: () => true,
        threshold: 3,
      })
      expect(isCollapsed()).toBe(false)
      expect(display()).toBe('a\nb\nc\nd\ne')
      dispose()
    })
  })

  it('clips lines longer than the per-line cap inside the collapsed slice', () => {
    createRoot((dispose) => {
      const longLine = 'x'.repeat(COLLAPSED_LINE_CHAR_CAP + 100)
      const { display, isCollapsed } = useCollapsedLines({
        text: () => `a\n${longLine}\nc\ndropped`,
        expanded: () => false,
        threshold: 3,
      })
      expect(isCollapsed()).toBe(true)
      expect(display()).toBe(`a\n${'x'.repeat(COLLAPSED_LINE_CHAR_CAP)}…\nc`)
      dispose()
    })
  })

  it('leaves lines exactly at the cap untouched', () => {
    createRoot((dispose) => {
      const atCap = 'y'.repeat(COLLAPSED_LINE_CHAR_CAP)
      const { display } = useCollapsedLines({
        text: () => `${atCap}\n${atCap}\n${atCap}\ndropped`,
        expanded: () => false,
        threshold: 3,
      })
      expect(display()).toBe(`${atCap}\n${atCap}\n${atCap}`)
      dispose()
    })
  })

  it('does not clip long lines when expanded', () => {
    createRoot((dispose) => {
      const longLine = 'x'.repeat(COLLAPSED_LINE_CHAR_CAP + 100)
      const text = `a\n${longLine}\nc\nd`
      const { display, isCollapsed } = useCollapsedLines({
        text: () => text,
        expanded: () => true,
        threshold: 3,
      })
      expect(isCollapsed()).toBe(false)
      expect(display()).toBe(text)
      dispose()
    })
  })

  it('returns an empty collapsed display for a zero-line threshold', () => {
    createRoot((dispose) => {
      const { display, isCollapsed } = useCollapsedLines({
        text: () => 'a\nb\nc',
        expanded: () => false,
        threshold: 0,
      })
      expect(isCollapsed()).toBe(true)
      expect(display()).toBe('')
      dispose()
    })
  })
})
