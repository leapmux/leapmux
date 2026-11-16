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
  const jump = deferred<boolean>()
  const jumpToLatest = vi.fn((_signal: AbortSignal) => jump.promise)
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
  const ctx = makeScrollContext({
    hasNewerMessages: () => hasNewer,
    cancelScrollAnimation: calls.cancelScrollAnimation,
    retakeScrollControl: calls.retakeScrollControl,
    rearmScrollBufferFiller: calls.rearmScrollBufferFiller,
    followTail: calls.followTail,
    leaveFollow: calls.leaveFollow,
    followRequested: () => requested,
    checkPosition: calls.checkPosition,
  })
  const follower = createChatTailFollower(ctx, {
    jumpToLatest,
    stickToBottom: calls.stickToBottom,
    animateToBottom: calls.animateToBottom,
  })
  return {
    follower,
    ctx,
    calls,
    jump,
    jumpToLatest,
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
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('keeps the bottom affordance accurate when a successful jump still has newer rows', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
  })

  it('keeps the position of a reader who scrolled during a successful jump', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setRequested(false)
    harness.setHasNewer(false)
    harness.jump.resolve(true)
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
    const first = deferred<boolean>()
    const second = deferred<boolean>()
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
    second.resolve(true)
    await settleJump(second.promise)
    first.reject(new Error('the latest page did not load'))
    await settleJump(first.promise)

    expect(leaveFollow).not.toHaveBeenCalled()
    expect(checkPosition).toHaveBeenCalledOnce()
  })

  it('passes the signal of the pending history jump to the fetch', () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()

    expect(harness.ctx.beginHistoryJump).toHaveBeenCalledOnce()
    expect(harness.jumpToLatest).toHaveBeenCalledWith(vi.mocked(harness.ctx.beginHistoryJump).mock.results[0]!.value)
  })

  it('takes the signal before it requests the tail, so its own jump does not count as pending there', () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()

    expect(vi.mocked(harness.ctx.beginHistoryJump).mock.invocationCallOrder[0]!)
      .toBeLessThan(harness.calls.followTail.mock.invocationCallOrder[0]!)
  })

  it('does not stick when a later history jump replaced the jump to the latest page', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    // Home starts its own jump, which aborts this one.
    harness.ctx.beginHistoryJump()
    expect(harness.follower.pendingJump()).toBeNull()
    harness.setHasNewer(false)
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
    expect(harness.calls.checkPosition).not.toHaveBeenCalled()
    expect(harness.follower.pendingJump()).toBeNull()
  })

  it('does nothing when the jump fails after it was abandoned', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.ctx.abandonHistoryJump()
    harness.jump.reject(new Error('aborted'))
    await settleJump(harness.jump.promise)

    expect(harness.calls.leaveFollow).not.toHaveBeenCalled()
    expect(harness.calls.checkPosition).not.toHaveBeenCalled()
  })

  it('sticks when another fetch delivered the tail', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setHasNewer(false)
    // Another history fetch of the store replaced this jump, and its page holds the tail.
    harness.jump.resolve(false)
    await settleJump(harness.jump.promise)

    expect(harness.calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('checks the position when another fetch replaced the jump and newer pages remain', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve(false)
    await settleJump(harness.jump.promise)

    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
  })

  it('abandons the pending history jump before it sticks with no newer page', () => {
    const harness = setup()

    harness.follower.forceScrollToBottom()

    expect(harness.ctx.abandonHistoryJump).toHaveBeenCalledOnce()
    expect(harness.ctx.beginHistoryJump).not.toHaveBeenCalled()
    expect(vi.mocked(harness.ctx.abandonHistoryJump).mock.invocationCallOrder[0]!)
      .toBeLessThan(harness.calls.stickToBottom.mock.invocationCallOrder[0]!)
  })

  it('abandons the pending history jump before the button animates with no newer page', () => {
    const harness = setup()

    harness.follower.scrollToBottom()

    expect(harness.ctx.abandonHistoryJump).toHaveBeenCalledOnce()
    expect(vi.mocked(harness.ctx.abandonHistoryJump).mock.invocationCallOrder[0]!)
      .toBeLessThan(harness.calls.animateToBottom.mock.invocationCallOrder[0]!)
  })

  it('selects a latest-page jump or local animation from window state', () => {
    const harness = setup({ hasNewer: false })
    harness.follower.scrollToBottom()
    expect(harness.calls.animateToBottom).toHaveBeenCalledOnce()

    harness.setHasNewer(true)
    harness.follower.scrollToBottom()
    expect(harness.calls.rearmScrollBufferFiller).toHaveBeenCalledOnce()
  })

  it('asks for the tail, with the stick as its placement, when the live tail is loaded', () => {
    const { follower, ctx, calls } = setup()

    follower.forceScrollToBottom()

    expect(ctx.requestPlace).toHaveBeenCalledOnce()
    const [place, show] = vi.mocked(ctx.requestPlace).mock.calls[0]!
    expect(place).toEqual({ kind: 'tail' })
    expect(show).toBeTypeOf('function')
    // The context shows the place at once, so the stick ran.
    expect(calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('does not stick while the tail waits for its rows', () => {
    const { follower, ctx, calls } = setup()
    // The rows of the tail are staged: the hook keeps the placement, and runs it later.
    let kept: (() => void) | undefined
    vi.mocked(ctx.requestPlace).mockImplementation((_place, show) => {
      kept = show
      return new Promise<boolean>(() => {})
    })

    follower.forceScrollToBottom()
    expect(calls.stickToBottom).not.toHaveBeenCalled()

    kept?.()
    expect(calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('asks for the tail, and starts no animation, for the button with rows staged below', () => {
    const { follower, ctx, calls } = setup()
    vi.mocked(ctx.stagedBelow).mockReturnValue(2)

    follower.scrollToBottom()

    // The animation moves inside the rows of the list, whose bottom is not the tail.
    expect(vi.mocked(ctx.requestPlace).mock.calls.map(([place]) => place)).toEqual([
      { kind: 'tail' },
    ])
    expect(calls.animateToBottom).not.toHaveBeenCalled()
  })

  it('asks for the tail, and starts no animation, for the button while no row is in the list', () => {
    const { follower, ctx, calls } = setup()
    vi.mocked(ctx.awaitingFirstView).mockReturnValue(true)

    follower.scrollToBottom()

    expect(vi.mocked(ctx.requestPlace).mock.calls.map(([place]) => place)).toEqual([
      { kind: 'tail' },
    ])
    expect(calls.animateToBottom).not.toHaveBeenCalled()
  })

  it('asks for the tail when the jump to the latest page settled, and not before', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    expect(harness.calls.followTail).toHaveBeenCalledOnce()
    expect(harness.ctx.requestPlace).not.toHaveBeenCalled()

    harness.setHasNewer(false)
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(vi.mocked(harness.ctx.requestPlace).mock.calls.map(([place]) => place)).toEqual([
      { kind: 'tail' },
    ])
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
      harness.jump.resolve(true)
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
      const first = deferred<boolean>()
      const second = deferred<boolean>()
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

      first.resolve(true)
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
