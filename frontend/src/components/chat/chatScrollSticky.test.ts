import { describe, expect, it, vi } from 'vitest'
import { createStickyBottom } from './chatScrollSticky'
import { makeFakeScrollDiv, makeScrollContext } from './useChatScroll.testkit'

/**
 * The two sticky writes over a fake element with `clientHeight` 500 and `scrollHeight` 5000,
 * so the bottom is at 4500, and spies on the context primitives that they call. `calls`
 * records the order of the calls.
 */
function setup(opts: { scrollTop?: number, clientHeight?: number, following?: boolean, animating?: boolean } = {}) {
  const div = makeFakeScrollDiv()
  div.setClientHeight(opts.clientHeight ?? 500)
  div.setScrollHeight(5000)
  div.setScrollTop(opts.scrollTop ?? 4000)
  const calls: string[] = []
  // Writes as the hook's writer does; the record is the hook's own concern.
  const writeOwnScrollTop = vi.fn((top: number, _opts: { refresh: boolean }) => {
    calls.push('writeOwnScrollTop')
    div.el.scrollTop = top
  })
  const sticky = createStickyBottom(makeScrollContext({
    getEl: () => div.el,
    isFollowing: () => opts.following ?? true,
    isAnimating: () => opts.animating ?? false,
    followTail: () => {
      calls.push('followTail')
    },
    noteScrollRange: () => {
      calls.push('noteScrollRange')
    },
    writeOwnScrollTop,
  }))
  return {
    div,
    calls,
    writeOwnScrollTop,
    ...sticky,
  }
}

describe('createStickyBottom', () => {
  describe('stickToBottom', () => {
    it('enters follow mode before it writes the bottom with a refresh of the rows', () => {
      const { div, calls, writeOwnScrollTop, stickToBottom } = setup()

      expect(stickToBottom()).toBe(true)

      // The position check of the write then reads a reader who requests the tail.
      expect(calls).toEqual(['followTail', 'writeOwnScrollTop'])
      expect(writeOwnScrollTop).toHaveBeenCalledWith(5000, { refresh: true })
      expect(div.el.scrollTop).toBe(4500)
    })

    it('returns false and changes nothing for a hidden element', () => {
      const { calls, stickToBottom } = setup({ clientHeight: 0 })

      expect(stickToBottom()).toBe(false)

      expect(calls).toEqual([])
    })
  })

  describe('restickIfAtBottom', () => {
    it('writes the bottom with no refresh of the rows while follow mode applies', () => {
      const { div, calls, writeOwnScrollTop, restickIfAtBottom } = setup()

      restickIfAtBottom()

      // The `scroll` event of the write, or the deferred refresh of the geometry effect,
      // refreshes the rows.
      expect(calls).toEqual(['noteScrollRange', 'writeOwnScrollTop'])
      expect(writeOwnScrollTop).toHaveBeenCalledWith(5000, { refresh: false })
      expect(div.el.scrollTop).toBe(4500)
    })

    it('records the scroll range for a reader who does not follow, and writes nothing', () => {
      const { calls, restickIfAtBottom } = setup({ following: false })

      restickIfAtBottom()

      // The reader can enter follow mode later at a bottom that a shrink clamped. The record
      // keeps that clamp no move of the reader when the content grows again.
      expect(calls).toEqual(['noteScrollRange'])
    })

    it('does nothing while the scroll animation runs', () => {
      const { calls, restickIfAtBottom } = setup({ animating: true })

      restickIfAtBottom()

      expect(calls).toEqual([])
    })

    it('does nothing for a hidden element', () => {
      const { calls, restickIfAtBottom } = setup({ clientHeight: 0 })

      restickIfAtBottom()

      expect(calls).toEqual([])
    })
  })
})
