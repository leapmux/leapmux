import { describe, expect, it, vi } from 'vitest'
import { createChatTailFollower } from './chatScrollTailFollower'

function deferred() {
  let resolve!: () => void
  let reject!: () => void
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

/**
 * A follower over spies. The harness holds one value for the request for the tail: the
 * `followTail` stub sets it, the `leaveFollow` stub clears it, and `followRequested`
 * returns it.
 */
function setup(overrides: { hasNewer?: boolean } = {}) {
  let hasNewer = overrides.hasNewer ?? false
  let requested = false
  const jump = deferred()
  const calls = {
    cancelAnimation: vi.fn(),
    retakeControl: vi.fn(),
    rearmBuffer: vi.fn(),
    followTail: vi.fn(() => {
      requested = true
    }),
    leaveFollow: vi.fn(() => {
      requested = false
    }),
    checkAtBottom: vi.fn(),
    stickToBottom: vi.fn(),
    animateToBottom: vi.fn(),
  }
  const follower = createChatTailFollower({
    hasNewerMessages: () => hasNewer,
    cancelAnimation: calls.cancelAnimation,
    retakeControl: calls.retakeControl,
    rearmBuffer: calls.rearmBuffer,
    followTail: calls.followTail,
    leaveFollow: calls.leaveFollow,
    followRequested: () => requested,
    jumpToLatest: () => jump.promise,
    checkAtBottom: calls.checkAtBottom,
    stickToBottom: calls.stickToBottom,
    animateToBottom: calls.animateToBottom,
  })
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

/** Let the branches of a settled jump run. */
async function settle(promise: Promise<void>) {
  await promise.catch(() => undefined)
  await Promise.resolve()
}

describe('createChatTailFollower', () => {
  it('sticks immediately when the live tail is already loaded', () => {
    const { follower, calls } = setup()

    follower.forceScrollToBottom()

    expect(calls.cancelAnimation).toHaveBeenCalledOnce()
    expect(calls.retakeControl).toHaveBeenCalledOnce()
    expect(calls.stickToBottom).toHaveBeenCalledOnce()
    expect(calls.rearmBuffer).not.toHaveBeenCalled()
  })

  it('jumps to the latest page and sticks after a successful catch-up', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    expect(harness.calls.rearmBuffer).toHaveBeenCalledOnce()
    expect(harness.calls.followTail).toHaveBeenCalledOnce()
    harness.setHasNewer(false)
    harness.jump.resolve()
    await settle(harness.jump.promise)

    expect(harness.calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('keeps the bottom affordance accurate when a successful jump still has newer rows', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve()
    await settle(harness.jump.promise)

    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
  })

  it('keeps the position of a reader who scrolled during a successful jump', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setRequested(false)
    harness.setHasNewer(false)
    harness.jump.resolve()
    await settle(harness.jump.promise)

    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
    expect(harness.calls.checkAtBottom).not.toHaveBeenCalled()
  })

  it('leaves follow mode after a failed jump', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.reject()
    await settle(harness.jump.promise)

    expect(harness.calls.leaveFollow).toHaveBeenCalledOnce()
    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
    expect(harness.calls.leaveFollow.mock.invocationCallOrder[0]!)
      .toBeLessThan(harness.calls.checkAtBottom.mock.invocationCallOrder[0]!)
  })

  it('preserves a mid-flight user scroll when the jump fails', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.setRequested(false)
    harness.jump.reject()
    await settle(harness.jump.promise)

    expect(harness.calls.leaveFollow).not.toHaveBeenCalled()
    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
  })

  it('ignores a failed jump after a newer jump starts', async () => {
    let requested = false
    const first = deferred()
    const second = deferred()
    const jumps = [first, second]
    let jumpIndex = 0
    const leaveFollow = vi.fn()
    const checkAtBottom = vi.fn()
    const follower = createChatTailFollower({
      hasNewerMessages: () => true,
      cancelAnimation: vi.fn(),
      retakeControl: vi.fn(),
      rearmBuffer: vi.fn(),
      followTail: () => {
        requested = true
      },
      leaveFollow,
      followRequested: () => requested,
      jumpToLatest: () => jumps[jumpIndex++]!.promise,
      checkAtBottom,
      stickToBottom: vi.fn(),
      animateToBottom: vi.fn(),
    })

    follower.forceScrollToBottom()
    follower.forceScrollToBottom()
    second.resolve()
    await settle(second.promise)
    first.reject()
    await settle(first.promise)

    expect(leaveFollow).not.toHaveBeenCalled()
    expect(checkAtBottom).toHaveBeenCalledOnce()
  })

  it('selects a latest-page jump or local animation from window state', () => {
    const harness = setup({ hasNewer: false })
    harness.follower.scrollToBottom()
    expect(harness.calls.animateToBottom).toHaveBeenCalledOnce()

    harness.setHasNewer(true)
    harness.follower.scrollToBottom()
    expect(harness.calls.rearmBuffer).toHaveBeenCalledOnce()
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
      await settle(harness.jump.promise)
      expect(harness.follower.pendingJump()).toBeNull()
    })

    it('reports no jump after a failure', async () => {
      const harness = setup({ hasNewer: true })
      harness.follower.forceScrollToBottom()
      harness.jump.reject()
      await settle(harness.jump.promise)
      expect(harness.follower.pendingJump()).toBeNull()
    })

    it('reports the newer of two jumps', async () => {
      const first = deferred()
      const second = deferred()
      const jumps = [first, second]
      let jumpIndex = 0
      const follower = createChatTailFollower({
        hasNewerMessages: () => true,
        cancelAnimation: vi.fn(),
        retakeControl: vi.fn(),
        rearmBuffer: vi.fn(),
        followTail: vi.fn(),
        leaveFollow: vi.fn(),
        followRequested: () => true,
        jumpToLatest: () => jumps[jumpIndex++]!.promise,
        checkAtBottom: vi.fn(),
        stickToBottom: vi.fn(),
        animateToBottom: vi.fn(),
      })
      follower.forceScrollToBottom()
      const firstJump = follower.pendingJump()
      follower.forceScrollToBottom()
      const secondJump = follower.pendingJump()
      expect(secondJump).not.toBe(firstJump)
      expect(secondJump).toEqual(expect.any(Number))

      first.resolve()
      await settle(first.promise)
      expect(follower.pendingJump()).toBe(secondJump)
    })

    it('reports no jump after jumpToBottom', () => {
      const harness = setup({ hasNewer: true })
      harness.follower.forceScrollToBottom()
      expect(harness.follower.pendingJump()).not.toBeNull()
      harness.follower.jumpToBottom()
      expect(harness.follower.pendingJump()).toBeNull()
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
