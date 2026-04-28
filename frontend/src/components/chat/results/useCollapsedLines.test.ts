import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { useCollapsedLines } from './useCollapsedLines'

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
})
