import { describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { createChatTailFollower } from './chatScrollTailFollower'
import { makeScrollContext, settleJump } from './useChatScroll.testkit'

/**
 * A follower over spies. The harness holds one value for the request for the tail: the
 * `followTail` stub sets it, the `leaveFollow` stub clears it, and `followRequested`
 * returns it.
 */
function setup(overrides: { hasNewer?: boolean } = {}) {
  let hasNewer = overrides.hasNewer ?? false
  let requested = false
  const jump = deferred<void>()
  const calls = {
    cancelScrollAnimation: vi.fn(),
    retakeScrollControl: vi.fn(),
    rearmScrollBufferFiller: vi.fn(),
    followTail: vi.fn(() => {
      requested = true
    }),
    leaveFollow: vi.fn(() => {
      requested = false
    }),
    checkPosition: vi.fn(),
    stickToBottom: vi.fn(),
    animateToBottom: vi.fn(),
  }
  const follower = createChatTailFollower(
    makeScrollContext({
      hasNewerMessages: () => hasNewer,
      cancelScrollAnimation: calls.cancelScrollAnimation,
      retakeScrollControl: calls.retakeScrollControl,
      rearmScrollBufferFiller: calls.rearmScrollBufferFiller,
      followTail: calls.followTail,
      leaveFollow: calls.leaveFollow,
      followRequested: () => requested,
      checkPosition: calls.checkPosition,
    }),
    {
      jumpToLatest: () => jump.promise,
      stickToBottom: calls.stickToBottom,
      animateToBottom: calls.animateToBottom,
    },
  )
  return {
    follower,
    calls,
    jump,
    setHasNewer: (value: boolean) => {
      hasNewer = value
    },
    setRequested: (value: boolean) => {
      requested = value
    },
  }
}

describe('createChatTailFollower', () => {
  it('sticks immediately when the live tail is already loaded', () => {
    const { follower, calls } = setup()

    follower.forceScrollToBottom()

    expect(calls.cancelScrollAnimation).toHaveBeenCalledOnce()
    expect(calls.retakeScrollControl).toHaveBeenCalledOnce()
    expect(calls.stickToBottom).toHaveBeenCalledOnce()
    expect(calls.rearmScrollBufferFiller).not.toHaveBeenCalled()
  })

  it('jumps to the latest page and sticks after a successful catch-up', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    expect(harness.calls.rearmScrollBufferFiller).toHaveBeenCalledOnce()
    expect(harness.calls.followTail).toHaveBeenCalledOnce()
    harness.setHasNewer(false)
    harness.jump.resolve()
    await settleJump(harness.jump.promise)

    expect(harness.calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('keeps the bottom affordance accurate when a successful jump still has newer rows', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve()
    await settleJump(harness.jump.promise)

    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
  })

  it('keeps the position of a reader who scrolled during a successful jump', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setRequested(false)
    harness.setHasNewer(false)
    harness.jump.resolve()
    await settleJump(harness.jump.promise)

    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
    expect(harness.calls.checkPosition).not.toHaveBeenCalled()
  })

  it('leaves follow mode after a failed jump', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.reject(new Error('the latest page did not load'))
    await settleJump(harness.jump.promise)

    expect(harness.calls.leaveFollow).toHaveBeenCalledOnce()
    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
    expect(harness.calls.leaveFollow.mock.invocationCallOrder[0]!)
      .toBeLessThan(harness.calls.checkPosition.mock.invocationCallOrder[0]!)
  })

  it('preserves a mid-flight user scroll when the jump fails', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setRequested(false)
    harness.jump.reject(new Error('the latest page did not load'))
    await settleJump(harness.jump.promise)

    expect(harness.calls.leaveFollow).not.toHaveBeenCalled()
    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
  })

  it('ignores a failed jump after a newer jump starts', async () => {
    let requested = false
    const first = deferred<void>()
    const second = deferred<void>()
    const jumps = [first, second]
    let jumpIndex = 0
    const leaveFollow = vi.fn()
    const checkPosition = vi.fn()
    const follower = createChatTailFollower(
      makeScrollContext({
        hasNewerMessages: () => true,
        followTail: () => {
          requested = true
        },
        leaveFollow,
        followRequested: () => requested,
        checkPosition,
      }),
      {
        jumpToLatest: () => jumps[jumpIndex++]!.promise,
        stickToBottom: vi.fn(),
        animateToBottom: vi.fn(),
      },
    )

    follower.forceScrollToBottom()
    follower.forceScrollToBottom()
    second.resolve()
    await settleJump(second.promise)
    first.reject(new Error('the latest page did not load'))
    await settleJump(first.promise)

    expect(leaveFollow).not.toHaveBeenCalled()
    expect(checkPosition).toHaveBeenCalledOnce()
  })

  it('selects a latest-page jump or local animation from window state', () => {
    const harness = setup({ hasNewer: false })
    harness.follower.scrollToBottom()
    expect(harness.calls.animateToBottom).toHaveBeenCalledOnce()

    harness.setHasNewer(true)
    harness.follower.scrollToBottom()
    expect(harness.calls.rearmScrollBufferFiller).toHaveBeenCalledOnce()
  })

  describe('pendingJump', () => {
    it('reports no jump before a request', () => {
      const { follower } = setup({ hasNewer: true })
      expect(follower.pendingJump()).toBeNull()
    })

    it('reports the pending jump until it resolves', async () => {
      const harness = setup({ hasNewer: true })
      harness.follower.forceScrollToBottom()
      expect(harness.follower.pendingJump()).toEqual(expect.any(Number))
      harness.jump.resolve()
      await settleJump(harness.jump.promise)
      expect(harness.follower.pendingJump()).toBeNull()
    })

    it('reports no jump after a failure', async () => {
      const harness = setup({ hasNewer: true })
      harness.follower.forceScrollToBottom()
      harness.jump.reject(new Error('the latest page did not load'))
      await settleJump(harness.jump.promise)
      expect(harness.follower.pendingJump()).toBeNull()
    })

    it('reports the newer of two jumps', async () => {
      const first = deferred<void>()
      const second = deferred<void>()
      const jumps = [first, second]
      let jumpIndex = 0
      const follower = createChatTailFollower(
        makeScrollContext({
          hasNewerMessages: () => true,
          followRequested: () => true,
        }),
        {
          jumpToLatest: () => jumps[jumpIndex++]!.promise,
          stickToBottom: vi.fn(),
          animateToBottom: vi.fn(),
        },
      )
      follower.forceScrollToBottom()
      const firstJump = follower.pendingJump()
      follower.forceScrollToBottom()
      const secondJump = follower.pendingJump()
      expect(secondJump).not.toBe(firstJump)
      expect(secondJump).toEqual(expect.any(Number))

      first.resolve()
      await settleJump(first.promise)
      expect(follower.pendingJump()).toBe(secondJump)
    })

    it('reports no jump after scrollToBottom animates', () => {
      const harness = setup({ hasNewer: true })
      harness.follower.forceScrollToBottom()
      harness.setHasNewer(false)
      harness.follower.scrollToBottom()
      expect(harness.follower.pendingJump()).toBeNull()
    })
  })
})
