import { describe, expect, it, vi } from 'vitest'
import { installScrollTestEnv, mountScrollHarness } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500.

/** A latest-page jump that the test settles. */
function deferredJump() {
  let resolve!: () => void
  let reject!: (reason: Error) => void
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return {
    promise,
    resolve,
    reject,
    onJumpToLatest: vi.fn(() => promise),
  }
}

/** Let the branches of a settled jump run. */
async function settle(promise: Promise<void>) {
  await promise.catch(() => undefined)
  await Promise.resolve()
  await Promise.resolve()
}

const maxScrollTop = (h: ReturnType<typeof mountScrollHarness>) => h.div.el.scrollHeight - h.div.el.clientHeight

describe('useChatScroll follow mode', () => {
  it('follows at mount', () => {
    const h = mountScrollHarness()
    expect(h.div.getScrollTop()).toBe(4500)
    expect(h.hook.isFollowing()).toBe(true)
    expect(h.hook.atBottom()).toBe(true)
    h.dispose()
  })

  it('stays at the bottom when the tail grows', () => {
    const h = mountScrollHarness()
    h.growTailPinned(200)
    expect(h.div.getScrollTop()).toBe(maxScrollTop(h))
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('leaves on a scroll that moves up past the tolerance', () => {
    const h = mountScrollHarness()
    h.scrollTo(4490)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.hook.atBottom()).toBe(false)
    h.dispose()
  })

  it('stays for a move inside the tolerance', () => {
    const h = mountScrollHarness()
    h.scrollTo(4499)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('leaves after many small steps', () => {
    const h = mountScrollHarness()
    const following: boolean[] = []
    for (let step = 1; step <= 20; step++) {
      h.scrollTo(4500 - step * 0.5)
      following.push(h.hook.isFollowing())
    }
    expect(following[3]).toBe(true)
    expect(following[4]).toBe(false)
    h.dispose()
  })

  it('does not leave on a scroll event that did not move', () => {
    const h = mountScrollHarness()
    h.div.setScrollHeight(5200)
    h.hook.handlers.onScroll()
    h.hook.handlers.onScroll()
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('does not leave when the viewport shrinks before the stick', () => {
    const h = mountScrollHarness()
    h.div.setClientHeight(400)
    h.hook.handlers.onScroll()
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('leaves on an upward wheel with scroll room', () => {
    const h = mountScrollHarness()
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -10 }))
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('stays on an upward wheel without scroll room', () => {
    const h = mountScrollHarness({
      heights: [100, 100, 100],
    })
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -10 }))
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('enters when a scroll reaches the bottom', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(4000)
    expect(h.hook.isFollowing()).toBe(false)
    h.scrollTo(4499)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('does not enter at the loaded bottom while newer messages exist', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.hook.atBottom()).toBe(true)
    h.dispose()
  })

  it('does not follow a window that is paged away', () => {
    const h = mountScrollHarness({ hasNewer: true, scrollTopBeforeMount: 1000 })
    h.div.setScrollHeight(5100)
    h.hook.restickIfAtBottom()
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(1000)
    h.dispose()
  })

  it('does not start to follow when the newer pages arrive after a scroll', () => {
    const h = mountScrollHarness({ hasNewer: true, scrollTopBeforeMount: 1000 })
    h.scrollTo(1200)
    h.setHasNewer(false)
    h.div.setScrollHeight(5100)
    h.hook.restickIfAtBottom()
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(1200)
    h.dispose()
  })

  it('returns to the tail when a gap closes without a move of the reader', () => {
    const h = mountScrollHarness()
    h.setHasNewer(true)
    // The scroller rests, so this change rebases: an own write, which must not end the request.
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(1)
    h.changeBelow(300)
    h.setHasNewer(false)

    expect(h.div.getScrollTop()).toBe(maxScrollTop(h))
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the request when the browser clamps the bottom during a gap', () => {
    const h = mountScrollHarness()
    h.setHasNewer(true)
    // ChatView removes the tail siblings while newer messages exist: the content shrinks,
    // and the browser clamps the position at the loaded bottom.
    h.div.setScrollHeight(4900)
    expect(h.div.getScrollTop()).toBe(4400)
    h.hook.handlers.onScroll()
    h.changeBelow(300)
    h.setHasNewer(false)

    expect(h.div.getScrollTop()).toBe(4700)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('follows when the tail becomes live and a tail sibling appears in the same step', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    // ChatView adds the thinking indicator in the update that clears the flag.
    h.div.setScrollHeight(5040)
    h.setHasNewer(false)

    expect(h.hook.isFollowing()).toBe(true)
    expect(h.div.getScrollTop()).toBe(4540)
    h.dispose()
  })

  it('does not follow when the tail becomes live under a mount that never scrolled', () => {
    const h = mountScrollHarness({ hasNewer: true, scrollTopBeforeMount: 1000 })
    h.setHasNewer(false)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(1000)
    h.dispose()
  })

  it('does not enter follow mode for a hidden pane', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    h.div.setClientHeight(0)
    await h.resizeFlush()
    h.changeBelow(300)
    h.setHasNewer(false)
    h.div.setClientHeight(500)
    await h.resizeFlush()

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('does not take the loaded bottom for a request at a resize', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    h.div.setClientHeight(480)
    await h.resizeFlush()
    h.setHasNewer(false)
    h.changeBelow(300)

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('updates atBottom when a newer page lands below a reader at the loaded bottom', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    h.changeBelow(300)
    h.setHasNewer(false)

    expect(h.hook.atBottom()).toBe(false)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('follows when the tail becomes live under a reader at the loaded bottom', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    h.setHasNewer(false)
    h.changeBelow(300)

    expect(h.hook.isFollowing()).toBe(true)
    expect(h.div.getScrollTop()).toBe(4800)
    h.dispose()
  })

  it('does not return to the bottom after the reader scrolled up 20px', () => {
    const h = mountScrollHarness()
    h.scrollTo(4480)
    h.changeBelow(100)
    expect(h.div.getScrollTop()).toBe(4480)
    h.dispose()
  })

  it('re-sticks when a tail sibling grows', () => {
    const h = mountScrollHarness()
    h.updateViewport.mockClear()
    h.div.setScrollHeight(5100)
    h.hook.restickIfAtBottom()
    expect(h.div.getScrollTop()).toBe(4600)
    expect(h.updateViewport).not.toHaveBeenCalled()
    h.dispose()
  })

  it('does not re-stick an anchored reader', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(4000)
    h.div.setScrollHeight(5100)
    h.hook.restickIfAtBottom()
    expect(h.div.getScrollTop()).toBe(4000)
    h.dispose()
  })
})

describe('useChatScroll forceScrollToBottom', () => {
  it('sticks at once when the tail is loaded', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    expect(h.div.getScrollTop()).toBe(4500)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('jumps to the latest page when windowed away', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    expect(jump.onJumpToLatest).toHaveBeenCalledOnce()

    h.setHasNewer(false)
    jump.resolve()
    await settle(jump.promise)

    expect(h.div.getScrollTop()).toBe(maxScrollTop(h))
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('leaves follow mode when the jump fails', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()

    jump.reject(new Error('the latest page did not load'))
    await settle(jump.promise)

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('sticks after the jump when a row above the viewport measures during the request', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)

    h.setHasNewer(false)
    jump.resolve()
    await settle(jump.promise)

    expect(h.div.getScrollTop()).toBe(maxScrollTop(h))
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the position of a reader who scrolled during the jump', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.scrollTo(1000)

    h.setHasNewer(false)
    jump.resolve()
    await settle(jump.promise)

    expect(h.div.getScrollTop()).toBe(1000)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('keeps the position of a reader who scrolled down during the jump', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.scrollTo(2300)

    h.setHasNewer(false)
    jump.resolve()
    await settle(jump.promise)

    expect(h.div.getScrollTop()).toBe(2300)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('keeps the request when the pane is hidden and shown during the jump', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)
    await h.hideThenShow()

    h.setHasNewer(false)
    jump.resolve()
    await settle(jump.promise)

    // The show sticks to the loaded bottom, because the resize holds the request.
    expect(h.div.getScrollTop()).toBe(4560)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('saves a pending request for the tail as an at-bottom state', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)

    expect(h.hook.atBottom()).toBe(false)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: true, hasMoreNewer: true })
    h.dispose()
  })

  it('saves an anchor for a reader at the loaded bottom of a paged-away window', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)

    expect(h.hook.atBottom()).toBe(true)
    expect(h.hook.getScrollState()).toMatchObject({
      atBottom: false,
      hasMoreNewer: true,
      anchor: { id: 'r45', offsetWithinRow: 0 },
    })
    h.dispose()
  })

  it('leaves follow mode when the jump fails after a row above the viewport measured', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)

    jump.reject(new Error('the latest page did not load'))
    await settle(jump.promise)
    h.setHasNewer(false)

    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('re-checks the bottom when newer messages remain after the jump', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()

    jump.resolve()
    await settle(jump.promise)

    expect(h.div.getScrollTop()).toBe(2000)
    expect(h.hook.atBottom()).toBe(false)
    h.dispose()
  })

  it('selects the jump or the animation in scrollToBottom', async () => {
    const windowed = deferredJump()
    const away = mountScrollHarness({
      hasNewer: true,
      hookOptions: { onJumpToLatest: windowed.onJumpToLatest },
    })
    await away.anchoredAt(2000)
    away.hook.scrollToBottom()
    expect(windowed.onJumpToLatest).toHaveBeenCalledOnce()
    away.dispose()

    const live = deferredJump()
    const h = mountScrollHarness({
      hookOptions: { onJumpToLatest: live.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.scrollToBottom()
    for (let frame = 0; frame < 80 && h.div.getScrollTop() < maxScrollTop(h); frame++)
      await h.frame()

    expect(live.onJumpToLatest).not.toHaveBeenCalled()
    expect(h.div.getScrollTop()).toBe(maxScrollTop(h))
    h.dispose()
  })
})
