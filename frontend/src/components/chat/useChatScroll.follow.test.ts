import { batch, createMemo, createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { deferredJump, innerScrollerTarget, installScrollTestEnv, mountScrollHarness, mountWithPendingJump, settleJump } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500.

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
    h.changeBelow(200)
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
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

  describe('a viewport that grows taller under a follower', () => {
    // A send from the bottom. The composer grew while the reader wrote, so the list is
    // 400px tall and the follower is at its bottom, 4600. The send shrinks the composer:
    // the list grows back to 500px, and the browser clamps the position to 4500.
    async function followerWithGrownComposer() {
      const h = mountScrollHarness()
      h.div.setClientHeight(400)
      await h.resizeFlush()
      expect(h.div.getScrollTop()).toBe(4600)
      expect(h.hook.isFollowing()).toBe(true)
      return h
    }

    it('stays when the tail grows before the scroll event of the clamp arrives', async () => {
      const h = await followerWithGrownComposer()
      h.div.setClientHeight(500)
      expect(h.div.getScrollTop()).toBe(4500)
      // The thinking indicator mounts in the same frame, so the maximum position is 4540
      // when the scroll event of the clamp arrives: the view is 40px above the bottom.
      h.div.setScrollHeight(5040)

      h.hook.handlers.onScroll()

      expect(h.hook.isFollowing()).toBe(true)
      // The answer arrives, and the follower goes to the bottom with it.
      h.changeBelow(200)
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      h.dispose()
    })

    it('follows the tail that grew after the clamp, before any scroll event', async () => {
      const h = await followerWithGrownComposer()
      h.div.setClientHeight(500)
      h.div.setScrollHeight(5040)

      // ChatView calls this after the thinking indicator renders.
      expect(h.hook.isFollowing()).toBe(true)
      h.hook.restickIfAtBottom()

      expect(h.div.getScrollTop()).toBe(4540)
      h.hook.handlers.onScroll()
      expect(h.hook.isFollowing()).toBe(true)
      h.dispose()
    })

    it('reads the scroll event of the clamp as no move of the reader', async () => {
      const h = await followerWithGrownComposer()
      h.onMomentumScroll.mockClear()
      h.div.setClientHeight(500)
      h.div.setScrollHeight(5040)

      h.hook.handlers.onScroll()

      // A move of the reader with no pointer down reports momentum, and a clamp does not.
      expect(h.onMomentumScroll).not.toHaveBeenCalled()
      h.dispose()
    })

    it('leaves when the reader moves up farther than the viewport grew', async () => {
      const h = await followerWithGrownComposer()
      h.div.setClientHeight(500)
      h.div.setScrollHeight(5040)
      // The clamp accounts for 100px. The reader moved 30px more.
      h.div.setScrollTop(4470)

      expect(h.hook.isFollowing()).toBe(false)
      h.hook.handlers.onScroll()

      expect(h.hook.isFollowing()).toBe(false)
      expect(h.hook.atBottom()).toBe(false)
      h.dispose()
    })

    it('leaves on a move up after the scroll event of the clamp, which the growth no longer explains', async () => {
      const h = mountScrollHarness()
      h.div.setClientHeight(400)
      await h.resizeFlush()
      // The follower is at 4600. The list grows to 500px, which clamps the position to
      // 4500, and its event arrives. The reader then scrolls up by 60px with no change of
      // the viewport, so the growth explains none of it.
      h.div.setClientHeight(500)
      h.hook.handlers.onScroll()
      expect(h.hook.isFollowing()).toBe(true)

      h.scrollTo(4440)

      expect(h.hook.isFollowing()).toBe(false)
      h.dispose()
    })

    it('reads the clamp as no move for a reader near the bottom who does not follow', async () => {
      const h = mountScrollHarness()
      h.div.setClientHeight(400)
      await h.resizeFlush()
      // The reader leaves the bottom, 4600, for 4550 and rests there.
      await h.anchoredAt(4550)
      expect(h.hook.isFollowing()).toBe(false)
      h.onMomentumScroll.mockClear()

      // The list grows to 500px: the maximum is 4500, so the browser moves the reader by
      // 50px. The tail then grows by 40px before the scroll event arrives.
      h.div.setClientHeight(500)
      h.div.setScrollHeight(5040)
      expect(h.div.getScrollTop()).toBe(4500)
      h.hook.handlers.onScroll()

      expect(h.onMomentumScroll).not.toHaveBeenCalled()
      expect(h.hook.isFollowing()).toBe(false)
      expect(h.div.getScrollTop()).toBe(4500)
      h.dispose()
    })
  })

  it('stays when a check recorded the clamp of a content shrink, and the tail grows before its scroll event arrives', () => {
    const h = mountScrollHarness()
    h.onMomentumScroll.mockClear()
    // A tail sibling shrinks by 100px: the browser clamps the follower to 4400. The check
    // of a re-stick records that clamp, and it writes nothing, because the view is at the
    // bottom.
    h.div.setScrollHeight(4900)
    h.hook.restickIfAtBottom()
    expect(h.div.getScrollTop()).toBe(4400)
    // The tail grows by 300px, and the scroll event of the clamp arrives before a stick.
    h.div.setScrollHeight(5200)

    h.hook.handlers.onScroll()

    expect(h.hook.isFollowing()).toBe(true)
    expect(h.onMomentumScroll).not.toHaveBeenCalled()
    h.changeBelow(100)
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    h.dispose()
  })

  it('leaves on an upward wheel with scroll room', () => {
    const h = mountScrollHarness()
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -10 }))
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('stays on an upward wheel that a scroller inside the list takes', () => {
    const h = mountScrollHarness()
    const target = innerScrollerTarget(h.div.el, { scrollTop: 200 })
    h.div.el.addEventListener('wheel', h.hook.handlers.onWheel)
    target.dispatchEvent(new WheelEvent('wheel', { deltaY: -10, bubbles: true }))
    expect(h.hook.isFollowing()).toBe(true)
    // The view stays at the bottom when new output arrives below.
    h.changeBelow(200)
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    h.div.el.remove()
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
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
    h.dispose()
  })

  it('does not follow a window that is paged away', () => {
    const h = mountScrollHarness({ hasNewer: true, scrollTopBeforeMount: 1000 })
    h.div.setScrollHeight(5100)
    h.hook.restickIfAtBottom()
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(1000)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
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

    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
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

  it('reads follow mode from the request while the pane is hidden', () => {
    const h = mountScrollHarness()
    // A hidden pane reads its position as 0, which would read as a move up from the bottom.
    h.div.setClientHeight(0)
    h.div.setScrollTop(0)
    expect(h.hook.isFollowing()).toBe(true)

    h.setHasNewer(true)
    expect(h.hook.isFollowing()).toBe(false)
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
    // The rule holds the bottom of the content for a follower: the rows move up by the
    // growth, and the view stays at the bottom with no write while the scroller moves.
    expect(h.virt.layoutShift()).toBe(-300)
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    // The rebase at rest moves the shift into the scroll position.
    await h.rest()
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

describe('useChatScroll isFollowingTracked', () => {
  /** A memo over the tracked accessor, as the rail owner of ChatView is, with its run count. */
  function trackedFollowMemo(hook: { isFollowingTracked: () => boolean }) {
    let runs = 0
    const { following, dispose } = createRoot((dispose) => {
      const following = createMemo(() => {
        runs++
        return hook.isFollowingTracked()
      })
      return { following, dispose }
    })
    return {
      following,
      runs: () => runs,
      dispose,
    }
  }

  it('runs a memo again when the reader leaves the tail and when the reader enters it', () => {
    const h = mountScrollHarness()
    const memo = trackedFollowMemo(h.hook)
    expect(memo.following()).toBe(true)

    h.scrollTo(4000)
    expect(memo.following()).toBe(false)

    h.scrollTo(4500)
    expect(memo.following()).toBe(true)
    expect(memo.runs()).toBe(3)
    memo.dispose()
    h.dispose()
  })

  it('runs a memo again when the jump to the latest page settles under a reader place that stays the tail', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    const memo = trackedFollowMemo(h.hook)
    // The reader place is the tail already, and the jump did not settle.
    expect(memo.following()).toBe(false)
    h.setHasNewer(false)
    expect(memo.following()).toBe(false)

    jump.resolve(true)
    await settleJump(jump.promise)

    expect(memo.following()).toBe(true)
    expect(h.hook.isFollowing()).toBe(true)
    memo.dispose()
    h.dispose()
  })

  it('gives a computation that reads isFollowing no dependency on the reader place', () => {
    const h = mountScrollHarness()
    let runs = 0
    const dispose = createRoot((dispose) => {
      createMemo(() => {
        runs++
        return h.hook.isFollowing()
      })
      return dispose
    })
    expect(runs).toBe(1)

    h.scrollTo(4000)
    expect(h.hook.isFollowing()).toBe(false)

    expect(runs).toBe(1)
    dispose()
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
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    expect(jump.onJumpToLatest).toHaveBeenCalledOnce()

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('leaves follow mode when the jump fails', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)

    jump.reject(new Error('the latest page did not load'))
    await settleJump(jump.promise)

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(2000)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
    h.dispose()
  })

  it('sticks after the jump when a row above the viewport measures during the request', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    h.changeAbove(60)

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the request through the scroll event of a rebase during the jump', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    // A row above the viewport measures, the rebase at rest writes, and its event arrives.
    h.changeAbove(60)
    expect(h.rebaseCount()).toBe(1)
    h.hook.handlers.onScroll()

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    // The event moved nothing, so it ends no request, although the view sits above the bottom.
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the position of a reader who scrolled during the jump', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    h.scrollTo(1000)

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(1000)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('keeps the position of a reader who scrolled down during the jump', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    h.scrollTo(2300)

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(2300)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('keeps the position of a reader who pages down during the jump', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    // A page of 500 - 48 = 452px leaves the view above the loaded bottom.
    h.hook.pageScroll(1)
    expect(h.div.getScrollTop()).toBe(2452)

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(2452)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('keeps the request when the pane is hidden and shown during the jump', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    h.changeAbove(60)
    await h.hideThenShow()

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    // The show sticks to the loaded bottom, because the resize holds the request.
    expect(h.div.getScrollTop()).toBe(4560)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('saves a pending request for the tail as an at-bottom state', async () => {
    const { h } = await mountWithPendingJump(mountScrollHarness)
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
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    h.changeAbove(60)

    jump.reject(new Error('the latest page did not load'))
    await settleJump(jump.promise)
    h.setHasNewer(false)

    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('re-checks the bottom when newer messages remain after the jump', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)

    jump.resolve(true)
    await settleJump(jump.promise)

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
    for (let frame = 0; frame < 80 && h.div.getScrollTop() < h.maxScrollTop(); frame++)
      await h.frame()

    expect(live.onJumpToLatest).not.toHaveBeenCalled()
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    h.dispose()
  })
})

// Staged rows are loaded rows that the list does not hold yet: a hidden copy still
// measures them. The harness states what the admission reports beside the rows of its
// virtualizer (see mountScrollHarness).
describe('useChatScroll follow mode with staged rows', () => {
  it('does not follow at the bottom of the list while rows that it showed before are staged below', async () => {
    // After a width change, the rows below the list were in the list before. A reader at
    // the bottom end of the measured rows did not ask for the tail.
    const h = mountScrollHarness({ stagedNewer: true, holdsTail: false })
    await h.anchoredAt(2000)

    h.scrollTo(4500)

    expect(h.hook.atBottom()).toBe(true)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('follows at the bottom once the rows that were staged below joined', async () => {
    const h = mountScrollHarness({ stagedNewer: true, holdsTail: false })
    await h.anchoredAt(2000)
    h.scrollTo(4500)
    expect(h.hook.isFollowing()).toBe(false)

    h.setStagedNewer(false)
    h.setHoldsTail(true)
    h.hook.handlers.onScroll()

    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('follows when the reader reaches the bottom while a new message is staged', async () => {
    // Only a new message is staged below, so the rows of the list still hold the tail.
    const h = mountScrollHarness({ stagedNewer: true })
    await h.anchoredAt(2000)

    h.scrollTo(4500)
    expect(h.hook.isFollowing()).toBe(true)

    // The message joins: the list grows below, with no scroll event.
    h.setStagedNewer(false)
    h.changeBelow(300)
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    h.dispose()
  })

  it('does not follow when a newer page arrives under a reader at the loaded bottom', async () => {
    const h = mountScrollHarness({ hasNewer: true })
    await h.anchoredAt(4000)
    h.scrollTo(4500)

    // The page arrives: its rows are staged, and they are no new messages.
    batch(() => {
      h.setStagedNewer(true)
      h.setHoldsTail(false)
      h.setHasNewer(false)
    })

    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('keeps a follower in follow mode while a new message is staged', () => {
    const h = mountScrollHarness()
    expect(h.hook.isFollowing()).toBe(true)

    h.setStagedNewer(true)
    h.changeBelow(300)

    expect(h.hook.isFollowing()).toBe(true)
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    h.dispose()
  })

  it('keeps a follower in follow mode for a forced scroll to the bottom while a new message is staged', () => {
    // The target of a request would wait here: the new message is not measured yet.
    const h = mountScrollHarness({ holdTargets: true })
    expect(h.hook.isFollowing()).toBe(true)
    h.setStagedNewer(true)

    // A send and a control response force the scroll to the bottom.
    h.hook.forceScrollToBottom()

    // The reader has the tail already, so nothing waits: follow mode shows the message
    // when it is measured, and no row of the loaded bottom counts as loading.
    expect(h.hook.isFollowing()).toBe(true)
    expect(h.hook.atStagedBottomEdge()).toBe(false)
    expect(h.requestedTargets()).toEqual([])
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    h.dispose()
  })

  it('waits for the tail at a forced scroll to the bottom when rows that the list showed before are staged below', () => {
    const h = mountScrollHarness({ holdTargets: true })
    // After a width change, the rows below the rows of the list are no new messages.
    h.setStagedNewer(true)
    h.setHoldsTail(false)

    h.hook.forceScrollToBottom()

    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])
    expect(h.hook.isFollowing()).toBe(false)
    h.reachTarget()
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('waits for the tail, and sticks when it is reached, at a forced scroll to the bottom of a reader who scrolled up', async () => {
    const h = mountScrollHarness({ holdTargets: true })
    await h.anchoredAt(2000)
    h.setStagedNewer(true)

    h.hook.forceScrollToBottom()

    // The reader has no tail yet, so the view stays until the last rows are measured.
    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])
    expect(h.div.getScrollTop()).toBe(2000)

    h.reachTarget()
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('asks for the tail, and starts no animation, for the button while no row is in the list', async () => {
    const h = mountScrollHarness({ awaitingFirstView: true, holdTargets: true })
    const writes = h.div.scrollTopWrites()

    h.hook.scrollToBottom()
    await h.frame()
    await h.frame()

    // The first view asked for the tail, and the button asks again: no row of the list
    // holds a bottom that an animation could move to.
    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
      { kind: 'tail' },
    ])
    expect(h.div.scrollTopWrites()).toBe(writes)
    h.dispose()
  })

  it('enters follow mode again at the bottom of a window with no row', async () => {
    // Content outside the rows makes the list scroll: a banner, or a streaming block.
    // The admission reports a held tail for a window with no row.
    const h = mountScrollHarness({ heights: [], scrollHeight: 5000 })
    await h.anchoredAt(1000)
    expect(h.hook.isFollowing()).toBe(false)

    h.scrollTo(4500)

    expect(h.hook.atBottom()).toBe(true)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('asks for the top when a jump to the latest page fails during the first view', async () => {
    const jump = deferredJump()
    const h = mountScrollHarness({
      hasNewer: true,
      awaitingFirstView: true,
      holdTargets: true,
      hookOptions: {
        savedViewportScroll: () => ({ atBottom: true, hasMoreNewer: true }),
        onClearSavedViewportScroll: vi.fn(),
        onJumpToLatest: jump.onJumpToLatest,
      },
    })
    // The mount starts the jump, and asks for no place while it is pending.
    expect(jump.onJumpToLatest).toHaveBeenCalledOnce()
    expect(h.requestedTargets()).toEqual([])

    jump.reject(new Error('the latest page did not load'))
    await settleJump(jump.promise)

    // The request for the tail ended with its fetch, also during the first view.
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
    expect(h.requestedTargets().at(-1)).toEqual({ kind: 'top' })

    h.reachTarget()
    expect(h.div.getScrollTop()).toBe(0)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('waits for the rows of the latest page when the jump of End arrives on staged rows', async () => {
    const { h, jump } = await mountWithPendingJump(opts => mountScrollHarness({ ...opts, holdTargets: true }))

    // The store clears its newer-pages flag when the page arrives, before the landing of
    // the jump runs. The rows of the page are staged then, and they are no new messages,
    // so the rows of the list do not hold the tail.
    h.setStagedNewer(true)
    h.setHoldsTail(false)
    h.setHasNewer(false)
    h.changeBelow(300)
    expect(h.div.getScrollTop()).toBe(2000)
    expect(h.hook.isFollowing()).toBe(false)

    jump.resolve(true)
    await settleJump(jump.promise)
    expect(h.div.getScrollTop()).toBe(2000)
    expect(h.requestedTargets().at(-1)).toEqual({ kind: 'tail' })

    h.reachTarget()
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })
})
