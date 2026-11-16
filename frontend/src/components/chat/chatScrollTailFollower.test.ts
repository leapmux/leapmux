import type { ScrollContext } from './useChatScroll'
import { describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { createChatTailFollower } from './chatScrollTailFollower'
import { makeScrollContext, settleJump } from './useChatScroll.testkit'

/**
 * The arguments of each `requestPlace` call of the follower, in order. A request for the
 * tail holds the place alone: the follower passes no placement, so the hook sticks.
 */
const placeRequests = (ctx: ScrollContext) => vi.mocked(ctx.requestPlace).mock.calls
const TAIL_REQUEST = [
  { kind: 'tail' },
]

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
    followTail: vi.fn(() => {
      requested = true
    }),
    leaveFollow: vi.fn(() => {
      requested = false
    }),
    checkPosition: vi.fn(),
    animateToBottom: vi.fn(),
  }
  const ctx = makeScrollContext({
    hasNewerMessages: () => hasNewer,
    cancelScrollAnimation: calls.cancelScrollAnimation,
    retakeScrollControl: calls.retakeScrollControl,
    followTail: calls.followTail,
    leaveFollow: calls.leaveFollow,
    followRequested: () => requested,
    checkPosition: calls.checkPosition,
  })
  const follower = createChatTailFollower(ctx, {
    jumpToLatest,
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
  it('retakes control for the animated scroll of the button', () => {
    const { follower, calls } = setup()

    follower.scrollToBottom()

    expect(calls.animateToBottom).toHaveBeenCalledOnce()
    // The button is a command of the reader, as End is: it ends the pause of the buffer fill.
    expect(calls.retakeScrollControl).toHaveBeenCalledOnce()
  })

  it('asks for the tail at once when the live tail is already loaded', () => {
    const { follower, ctx, calls } = setup()

    follower.forceScrollToBottom()

    expect(calls.cancelScrollAnimation).toHaveBeenCalledOnce()
    expect(calls.retakeScrollControl).toHaveBeenCalledOnce()
    expect(placeRequests(ctx)).toEqual([TAIL_REQUEST])
    expect(ctx.beginHistoryJump).not.toHaveBeenCalled()
  })

  it('jumps to the latest page and asks for the tail after a successful catch-up', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    expect(harness.ctx.beginHistoryJump).toHaveBeenCalledOnce()
    expect(harness.calls.followTail).toHaveBeenCalledOnce()
    harness.setHasNewer(false)
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(placeRequests(harness.ctx)).toEqual([TAIL_REQUEST])
  })

  it('keeps the bottom affordance accurate when a successful jump still has newer rows', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
    expect(harness.ctx.requestPlace).not.toHaveBeenCalled()
  })

  it('keeps the position of a reader who scrolled during a successful jump', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setRequested(false)
    harness.setHasNewer(false)
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.ctx.requestPlace).not.toHaveBeenCalled()
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

  it('takes the signal while no jump of its own counts as pending', () => {
    // The hook ends the request for the tail of a pending jump to the latest page when a
    // new history jump begins. The jump that begins must not count as that pending jump,
    // and neither must the jump that it replaces.
    const pendingAtBegin: Array<number | null> = []
    // The follower needs the context, and the context reads the follower: the read
    // goes through this function, which the follower replaces once it exists.
    let pendingJump: () => number | null = () => null
    const ctx = makeScrollContext({
      hasNewerMessages: () => true,
      beginHistoryJump: vi.fn(() => {
        pendingAtBegin.push(pendingJump())
        return new AbortController().signal
      }),
    })
    const follower = createChatTailFollower(ctx, {
      jumpToLatest: () => new Promise<boolean>(() => {}),
      animateToBottom: vi.fn(),
    })
    pendingJump = follower.pendingJump

    follower.forceScrollToBottom()
    follower.forceScrollToBottom()

    expect(pendingAtBegin).toEqual([null, null])
    expect(follower.pendingJump()).toEqual(expect.any(Number))
  })

  it('asks for no tail when a later history jump replaced the jump to the latest page', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    // Home starts its own jump, which aborts this one.
    harness.ctx.beginHistoryJump()
    expect(harness.follower.pendingJump()).toBeNull()
    harness.setHasNewer(false)
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.ctx.requestPlace).not.toHaveBeenCalled()
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

  it('asks for the tail when another fetch delivered the tail', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setHasNewer(false)
    // Another history fetch of the store replaced this jump, and its page holds the tail.
    harness.jump.resolve(false)
    await settleJump(harness.jump.promise)

    expect(placeRequests(harness.ctx)).toEqual([TAIL_REQUEST])
  })

  it('checks the position when another fetch replaced the jump and newer pages remain', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve(false)
    await settleJump(harness.jump.promise)

    expect(harness.calls.checkPosition).toHaveBeenCalledOnce()
    expect(harness.ctx.requestPlace).not.toHaveBeenCalled()
  })

  it('abandons the pending history jump before it asks for the tail with no newer page', () => {
    const harness = setup()

    harness.follower.forceScrollToBottom()

    expect(harness.ctx.abandonHistoryJump).toHaveBeenCalledOnce()
    expect(harness.ctx.beginHistoryJump).not.toHaveBeenCalled()
    expect(vi.mocked(harness.ctx.abandonHistoryJump).mock.invocationCallOrder[0]!)
      .toBeLessThan(vi.mocked(harness.ctx.requestPlace).mock.invocationCallOrder[0]!)
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
    expect(harness.ctx.beginHistoryJump).toHaveBeenCalledOnce()
  })

  it('asks for the tail, and starts no animation, for the button with rows staged below', () => {
    const { follower, ctx, calls } = setup()
    // The last loaded row is not in the list: rows are staged below, or no row is in it.
    vi.mocked(ctx.stagedNewer).mockReturnValue(true)

    follower.scrollToBottom()

    // The animation moves inside the rows of the list, whose bottom is not the tail.
    expect(placeRequests(ctx)).toEqual([TAIL_REQUEST])
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

    expect(placeRequests(harness.ctx)).toEqual([TAIL_REQUEST])
  })

  it('ends the pending history jump of the hook when its jump settles', async () => {
    const harness = setup({ hasNewer: true })
    harness.follower.forceScrollToBottom()
    const signal = vi.mocked(harness.ctx.beginHistoryJump).mock.results[0]!.value

    harness.setHasNewer(false)
    harness.jump.resolve(true)
    await settleJump(harness.jump.promise)

    expect(harness.ctx.settleHistoryJump).toHaveBeenCalledWith(signal)
    // The hook learns of the end before the landing asks for the tail.
    expect(vi.mocked(harness.ctx.settleHistoryJump).mock.invocationCallOrder[0]!)
      .toBeLessThan(vi.mocked(harness.ctx.requestPlace).mock.invocationCallOrder[0]!)
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
