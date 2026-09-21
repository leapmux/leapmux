import type { ScrollAnchor } from '~/stores/chatTypes'
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

function setup(overrides: { hasNewer?: boolean, atBottom?: boolean, anchor?: ScrollAnchor | null } = {}) {
  let hasNewer = overrides.hasNewer ?? false
  let atBottom = overrides.atBottom ?? true
  const jump = deferred()
  const calls = {
    cancelAnimation: vi.fn(),
    retakeControl: vi.fn(),
    rearmBuffer: vi.fn(),
    followTail: vi.fn(),
    setAtBottom: vi.fn((value: boolean) => { atBottom = value }),
    checkAtBottom: vi.fn(),
    stickToBottom: vi.fn(),
    setAnchor: vi.fn(),
    animateToBottom: vi.fn(),
  }
  const follower = createChatTailFollower({
    getEl: () => ({ scrollTop: 25 } as HTMLDivElement),
    hasNewerMessages: () => hasNewer,
    cancelAnimation: calls.cancelAnimation,
    retakeControl: calls.retakeControl,
    rearmBuffer: calls.rearmBuffer,
    currentAnchorState: () => overrides.anchor === undefined
      ? null
      : overrides.anchor === null ? null : { anchor: overrides.anchor, viewportOffsetRatio: 0.25 },
    followTail: calls.followTail,
    atBottomSnapshot: () => atBottom,
    setAtBottom: calls.setAtBottom,
    jumpToLatest: () => jump.promise,
    checkAtBottom: calls.checkAtBottom,
    stickToBottom: calls.stickToBottom,
    setAnchor: calls.setAnchor,
    anchorAtCurrentTop: () => ({ id: 'current', offsetWithinRow: 4 }),
    animateToBottom: calls.animateToBottom,
  })
  const setHasNewer = (value: boolean) => {
    hasNewer = value
  }
  const setAtBottom = (value: boolean) => {
    atBottom = value
  }
  return { follower, calls, jump, setHasNewer, setAtBottom }
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
    await harness.jump.promise
    await Promise.resolve()

    expect(harness.calls.stickToBottom).toHaveBeenCalledOnce()
  })

  it('keeps the bottom affordance accurate when a successful jump still has newer rows', async () => {
    const harness = setup({ hasNewer: true })

    harness.follower.forceScrollToBottom()
    harness.jump.resolve()
    await harness.jump.promise
    await Promise.resolve()

    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
    expect(harness.calls.stickToBottom).not.toHaveBeenCalled()
  })

  it('restores the prior anchor after a failed jump', async () => {
    const anchor: ScrollAnchor = { id: 'before', offsetWithinRow: 8 }
    const harness = setup({ hasNewer: true, anchor })

    harness.follower.forceScrollToBottom()
    harness.jump.reject()
    await harness.jump.promise.catch(() => undefined)
    await Promise.resolve()

    expect(harness.calls.setAnchor).toHaveBeenCalledWith(anchor, undefined, 0.25)
    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
  })

  it('preserves a mid-flight user scroll when the jump fails', async () => {
    const harness = setup({ hasNewer: true, anchor: { id: 'before', offsetWithinRow: 8 } })

    harness.follower.forceScrollToBottom()
    harness.setAtBottom(false)
    harness.jump.reject()
    await harness.jump.promise.catch(() => undefined)
    await Promise.resolve()

    expect(harness.calls.setAnchor).not.toHaveBeenCalled()
    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
  })

  it('ignores a failed jump after a newer jump starts', async () => {
    let atBottom = true
    const first = deferred()
    const second = deferred()
    const jumps = [first, second]
    let jumpIndex = 0
    const setAnchor = vi.fn()
    const checkAtBottom = vi.fn()
    const follower = createChatTailFollower({
      getEl: () => ({ scrollTop: 25 } as HTMLDivElement),
      hasNewerMessages: () => true,
      cancelAnimation: vi.fn(),
      retakeControl: vi.fn(),
      rearmBuffer: vi.fn(),
      currentAnchorState: () => ({ anchor: { id: `before-${jumpIndex}`, offsetWithinRow: 0 }, viewportOffsetRatio: 0 }),
      followTail: vi.fn(),
      atBottomSnapshot: () => atBottom,
      setAtBottom: (value) => { atBottom = value },
      jumpToLatest: () => jumps[jumpIndex++]!.promise,
      checkAtBottom,
      stickToBottom: vi.fn(),
      setAnchor,
      anchorAtCurrentTop: () => null,
      animateToBottom: vi.fn(),
    })

    follower.forceScrollToBottom()
    follower.forceScrollToBottom()
    second.resolve()
    await second.promise
    await Promise.resolve()
    first.reject()
    await first.promise.catch(() => undefined)
    await Promise.resolve()

    expect(setAnchor).not.toHaveBeenCalled()
    expect(checkAtBottom).toHaveBeenCalledOnce()
  })

  it('anchors at the current viewport after a failed jump from follow mode', async () => {
    const harness = setup({ hasNewer: true, anchor: null })

    harness.follower.forceScrollToBottom()
    harness.jump.reject()
    await harness.jump.promise.catch(() => undefined)
    await Promise.resolve()

    expect(harness.calls.setAnchor).toHaveBeenCalledWith({ id: 'current', offsetWithinRow: 4 })
    expect(harness.calls.checkAtBottom).toHaveBeenCalledOnce()
  })

  it('selects a latest-page jump or local animation from window state', () => {
    const harness = setup({ hasNewer: false })
    harness.follower.scrollToBottom()
    expect(harness.calls.animateToBottom).toHaveBeenCalledOnce()

    harness.setHasNewer(true)
    harness.follower.scrollToBottom()
    expect(harness.calls.rearmBuffer).toHaveBeenCalledOnce()
  })
})
