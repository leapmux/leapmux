import type { ChatScrollState } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { FOLLOW_TOLERANCE_PX } from './chatScrollGeometry'
import { HARNESS_EVENT_GAP_MS, installScrollTestEnv, mountScrollHarness } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500. The hook
// follows the tail at the mount. While the viewport top lies below row 0, `changeAbove(px)`
// grows row 0 and moves the layer by `-px`, as the virtualizer keeps the rows under the
// reader in place.

describe('useChatScroll geometry change', () => {
  it('writes no scroll position for a change above the viewport during a scroll', () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.scrollTo(1990)
    const writes = h.div.scrollTopWrites()

    h.changeAbove(60)

    expect(h.div.getScrollTop()).toBe(1990)
    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.rebaseCount()).toBe(0)
    expect(h.virt.layoutShift()).toBe(-60)
    h.dispose()
  })

  it('writes no scroll position while a pointer is down', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))

    h.changeAbove(60)
    await h.rest()
    await h.rest()

    expect(h.rebaseCount()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('rebases after the change when the scroller is at rest', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)

    h.changeAbove(60)

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('rebases at rest after a change that keeps the total height', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    // Row 0 grows and the last row shrinks by the same height: the total does not change,
    // and only the geometry version reports the change to the geometry effect.
    h.changeAboveKeepingTotal(60)

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('rebases when the pointer goes up after a change during the press', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(0)

    document.body.dispatchEvent(new PointerEvent('pointerup', { pointerId: 1, isPrimary: true, bubbles: true }))

    expect(h.rebaseCount()).toBe(1)
    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })

  it('refreshes the rendered rows in a later frame, not in the effect', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.updateViewport.mockClear()

    h.changeAbove(60)
    expect(h.updateViewport).not.toHaveBeenCalled()

    await h.frame()
    expect(h.updateViewport).toHaveBeenCalledOnce()
    h.dispose()
  })

  it('drops the pending refresh of the rendered rows at the unmount', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.changeAbove(60)
    h.updateViewport.mockClear()

    h.dispose()
    await h.frame()

    expect(h.updateViewport).not.toHaveBeenCalled()
  })

  it('returns to the same row after the pane was hidden', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.changeAbove(60)
    expect(h.div.getScrollTop()).toBe(2060)

    h.div.setClientHeight(0)
    await h.resizeFlush()
    // A browser reads scrollTop as 0 while `display: none` hides the pane, and it can keep
    // 0 when the pane shows again: only the restore returns the view to the row.
    h.div.setScrollTop(0)
    h.div.setClientHeight(500)
    await h.resizeFlush()

    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })
})

// A browser reads clientHeight and scrollTop as 0 while `display: none` hides the pane.
describe('useChatScroll hidden pane', () => {
  it('stops the rest detection when the pane hides during the quiet time', async () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    h.div.setClientHeight(0)
    h.div.setScrollTop(0)

    await h.rest()
    await h.rest()

    // A confirm frame that reads 0 as a move on its way starts the quiet time again, for
    // as long as the pane stays hidden.
    expect(vi.getTimerCount()).toBe(0)
    h.dispose()
  })

  it('records nothing for a scroll event of a hidden pane', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.updateViewport.mockClear()
    h.onMomentumScroll.mockClear()
    h.setFastScrollActive.mockClear()

    h.div.setClientHeight(0)
    h.div.setScrollTop(0)
    h.hook.handlers.onScroll()

    expect(h.updateViewport).not.toHaveBeenCalled()
    expect(h.setFastScrollActive).not.toHaveBeenCalledWith(true)
    expect(h.onMomentumScroll).not.toHaveBeenCalled()

    // The pane shows at its position: the event reads as no move.
    h.div.setClientHeight(500)
    h.div.setScrollTop(2000)
    h.hook.handlers.onScroll()
    expect(h.onMomentumScroll).not.toHaveBeenCalled()
    h.dispose()
  })
})

// A touch or scrollbar drag moves the view before the handler sees its first `scroll`
// event. Each test below starts with a follower at 4500 and moves the view with no event.
describe('useChatScroll unseen upward move', () => {
  /** `count` messages with the ids and the seqs of the harness rows. */
  const rowMessages = (count: number) => Array.from({ length: count }, (_, i) => ({
    id: `r${i}`,
    seq: BigInt(i + 1),
  } as AgentChatMessage))

  it('keeps the view for a change above the viewport', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(100)
    const writes = h.div.scrollTopWrites()

    h.changeAbove(60)

    expect(h.div.getScrollTop()).toBe(100)
    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.rebaseCount()).toBe(0)
    // The rule holds row 1 under the viewport top.
    expect(h.virt.layoutShift()).toBe(-60)
    h.dispose()
  })

  it('keeps the view when the content grows below', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(100)

    h.changeBelow(300)

    expect(h.div.getScrollTop()).toBe(100)
    // Follow mode does not apply during the move, so the rule holds the rows under the
    // reader. Holding the bottom would move them up by 300px under the drag.
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('reads no follow mode during the move, and saves the request until its event', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(100)

    expect(h.hook.isFollowing()).toBe(false)
    // The request stands until the event of the move ends it.
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: true })

    vi.advanceTimersByTime(HARNESS_EVENT_GAP_MS)
    h.hook.handlers.onScroll()

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
    h.dispose()
  })

  it('reads follow mode again when the view returns before the event of the move', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(100)
    expect(h.hook.isFollowing()).toBe(false)

    h.div.setScrollTop(4500)

    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the view for a new message', () => {
    const h = mountScrollHarness({
      messages: rowMessages(50),
    })
    h.div.setScrollTop(100)

    h.setMessages(rowMessages(51))

    expect(h.div.getScrollTop()).toBe(100)
    h.dispose()
  })

  it('leaves follow mode when the event of the move arrives after the change', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(100)
    h.changeAbove(60)

    vi.advanceTimersByTime(HARNESS_EVENT_GAP_MS)
    h.hook.handlers.onScroll()

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.hook.atBottom()).toBe(false)
    // The next growth below does not move the view either.
    h.changeBelow(300)
    expect(h.div.getScrollTop()).toBe(100)
    h.dispose()
  })

  it('sticks for an explicit command', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(100)

    h.hook.forceScrollToBottom()

    expect(h.div.getScrollTop()).toBe(4500)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('sticks after a move of at most the follow tolerance', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(4500 - FOLLOW_TOLERANCE_PX)

    h.changeBelow(300)

    expect(h.div.getScrollTop()).toBe(4800)
    h.dispose()
  })

  it('keeps the view after a move just past the follow tolerance', () => {
    const h = mountScrollHarness()
    h.div.setScrollTop(4500 - FOLLOW_TOLERANCE_PX - 1)

    h.changeBelow(300)

    expect(h.div.getScrollTop()).toBe(4500 - FOLLOW_TOLERANCE_PX - 1)
    // The rule holds the rows under the reader, not the bottom.
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('holds the bottom when a row grows after a shrink clamped the view', async () => {
    const h = mountScrollHarness()
    // A tail sibling of the spacer shrinks: the browser clamps scrollTop to the new maximum,
    // 4400, and its event is late. A row shrink would clamp nothing: the rule holds the
    // bottom of a follower with a positive shift.
    h.div.setScrollHeight(4900)
    expect(h.div.getScrollTop()).toBe(4400)

    h.changeBelow(300)

    // The clamp is no move of the reader, so follow mode applies, and the rule holds the
    // bottom: the view stays at the maximum position.
    expect(h.hook.isFollowing()).toBe(true)
    expect(h.virt.layoutShift()).toBe(-300)
    expect(h.div.getScrollTop()).toBe(4400)

    // The late event of the clamp ends nothing, and the rebase at rest keeps the bottom.
    vi.advanceTimersByTime(HARNESS_EVENT_GAP_MS)
    h.hook.handlers.onScroll()
    expect(h.hook.isFollowing()).toBe(true)
    await h.rest()
    expect(h.div.getScrollTop()).toBe(4700)
    expect(h.div.getScrollTop()).toBe(h.div.el.scrollHeight - h.div.el.clientHeight)
    h.dispose()
  })

  it('sticks when a tail sibling grows again after a shrink that a restick saw', () => {
    const h = mountScrollHarness()
    // A tail sibling of the spacer shrinks, and ChatView calls restickIfAtBottom.
    h.div.setScrollHeight(4900)
    h.hook.restickIfAtBottom()
    expect(h.div.getScrollTop()).toBe(4400)

    h.div.setScrollHeight(5200)
    h.hook.restickIfAtBottom()

    expect(h.div.getScrollTop()).toBe(4700)
    h.dispose()
  })

  it('sticks when the content grows again after a shrink that the stick for a new message saw', () => {
    const h = mountScrollHarness({
      messages: rowMessages(50),
    })
    h.div.setScrollHeight(4900)
    h.setMessages(rowMessages(51))
    expect(h.div.getScrollTop()).toBe(4400)

    // A tail sibling grows. A row that grows would need no stick: the rule holds the bottom
    // of a follower.
    h.div.setScrollHeight(5200)
    h.hook.restickIfAtBottom()

    expect(h.div.getScrollTop()).toBe(4700)
    h.dispose()
  })

  it('sticks when the content grows after a shrink clamped a reader who did not follow', () => {
    const h = mountScrollHarness()
    h.scrollTo(4450)
    expect(h.hook.isFollowing()).toBe(false)
    // The content below shrinks: the browser clamps the view to the new bottom, 4400, and its
    // event is late. The position check of the geometry effect enters follow mode there.
    h.changeBelow(-100)
    expect(h.div.getScrollTop()).toBe(4400)
    expect(h.hook.isFollowing()).toBe(true)

    // A tail sibling grows before the event of the clamp: the clamp is no move of the reader.
    h.div.setScrollHeight(5200)
    h.hook.restickIfAtBottom()
    expect(h.div.getScrollTop()).toBe(4700)

    // The late event of the clamp then ends nothing.
    vi.advanceTimersByTime(HARNESS_EVENT_GAP_MS)
    h.hook.handlers.onScroll()
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('sticks when a tail sibling grows again after a shrink that a position check saw', async () => {
    const [saved, setSaved] = createSignal<ChatScrollState | undefined>()
    const h = mountScrollHarness({
      hookOptions: { savedViewportScroll: saved },
    })
    h.scrollTo(4450)
    expect(h.hook.isFollowing()).toBe(false)
    // The host clears a saved state, and the position check runs in the next frame.
    setSaved({ atBottom: false, hasMoreNewer: false })
    setSaved(undefined)
    // Before that frame, a tail sibling shrinks: the browser clamps the view to the new
    // bottom, 4400, and its event is late. The check enters follow mode there.
    h.div.setScrollHeight(4900)
    await h.frame()
    expect(h.div.getScrollTop()).toBe(4400)
    expect(h.hook.isFollowing()).toBe(true)

    // A tail sibling grows before the event of the clamp: the clamp is no move of the reader.
    h.div.setScrollHeight(5200)
    h.hook.restickIfAtBottom()

    expect(h.div.getScrollTop()).toBe(4700)
    h.dispose()
  })

  it('keeps the view after a move above the position that a shrink clamped', () => {
    const h = mountScrollHarness()
    // A tail sibling shrinks, and the browser clamps the view to 4400.
    h.div.setScrollHeight(4900)
    h.div.setScrollTop(4000)

    h.changeBelow(300)

    expect(h.div.getScrollTop()).toBe(4000)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })
})
