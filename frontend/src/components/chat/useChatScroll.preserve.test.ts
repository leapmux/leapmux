import type { ScrollHarnessOptions } from './useChatScroll.testkit'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { withChatScrollPreserved } from './chatScrollPreserve'
import { installScrollTestEnv, mountScrollHarness } from './useChatScroll.testkit'

installScrollTestEnv()

// The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500. Its element is
// attached to the document as a chat scroll container, so withChatScrollPreserved finds the
// controller that the hook registered for it.

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

function mountChat(opts: ScrollHarnessOptions = {}) {
  const h = mountScrollHarness(opts)
  h.div.el.setAttribute('data-chat-scroll-container', 'true')
  document.body.append(h.div.el)
  /**
   * Run `work` under the repair, and return the count of `scrollTop` writes at the end of
   * the work, before the restore. The restore waits for two frames, and this harness runs
   * each frame on a microtask, so the restore can run before this promise settles. A count
   * read after this call can therefore include the writes of the restore.
   */
  const preserved = async (work: () => Promise<void> | void): Promise<number> => {
    let writesAfterWork = 0
    await withChatScrollPreserved(async () => {
      await work()
      writesAfterWork = h.div.scrollTopWrites()
    })
    return writesAfterWork
  }
  /** Run two more frames, so that the restore ran in any order of the microtasks. */
  const restoreFrames = async () => {
    await h.frame()
    await h.frame()
  }
  /** The work of the browser bug that the repair exists for: a reset of the position to 0. */
  const reset = () => {
    h.div.setScrollTop(0)
    h.hook.handlers.onScroll()
  }
  return {
    ...h,
    preserved,
    restoreFrames,
    reset,
  }
}

/** Let the branches of a settled jump run. */
async function settle(promise: Promise<void>) {
  await promise.catch(() => undefined)
  await Promise.resolve()
  await Promise.resolve()
}

afterEach(() => {
  document.body.replaceChildren()
})

describe('useChatScroll scroll controller', () => {
  it('returns a follower to the bottom after a reset', async () => {
    const h = mountChat()
    await h.preserved(h.reset)
    await h.restoreFrames()

    expect(h.div.getScrollTop()).toBe(4500)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('returns an anchored reader to the row after a reset', async () => {
    const h = mountChat()
    await h.anchoredAt(2000)
    await h.preserved(h.reset)
    await h.restoreFrames()

    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('restores when the event of the reset did not arrive yet', async () => {
    const h = mountChat()
    await h.anchoredAt(2000)
    await h.preserved(() => h.div.setScrollTop(0))
    await h.restoreFrames()

    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('restores under a shift', async () => {
    const h = mountChat()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    await h.preserved(() => {
      h.changeAbove(300)
      h.reset()
    })
    await h.restoreFrames()

    expect(h.virt.layoutShift()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2290)
    h.dispose()
  })

  it('stays at the loaded bottom while newer messages exist', async () => {
    const h = mountChat({ hasNewer: true })
    await h.anchoredAt(4500)
    await h.preserved(h.reset)
    await h.restoreFrames()

    expect(h.div.getScrollTop()).toBe(4500)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('keeps a pending request for the tail across a reset', async () => {
    const jump = deferredJump()
    const h = mountChat({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)
    await h.preserved(h.reset)
    await h.restoreFrames()
    // The position of the snapshot.
    expect(h.div.getScrollTop()).toBe(2060)

    h.changeBelow(300)
    h.setHasNewer(false)
    jump.resolve()
    await settle(jump.promise)

    // A restore that drops the request leaves 2060.
    expect(h.div.getScrollTop()).toBe(4860)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the position when the jump fails after a restore', async () => {
    const jump = deferredJump()
    const h = mountChat({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)
    await h.preserved(h.reset)
    await h.restoreFrames()

    jump.reject(new Error('the latest page did not load'))
    await settle(jump.promise)

    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('drops the request when the jump fails during the work', async () => {
    const jump = deferredJump()
    const h = mountChat({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    await h.anchoredAt(2000)
    h.hook.forceScrollToBottom()
    h.changeAbove(60)
    await h.preserved(async () => {
      h.reset()
      jump.reject(new Error('the latest page did not load'))
      await settle(jump.promise)
    })
    await h.restoreFrames()

    expect(h.div.getScrollTop()).toBe(2060)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })

    h.div.setClientHeight(480)
    await h.resizeFlush()
    // A restore that sets the request again saves an at-bottom state, and the resize sticks.
    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })

  it('restores a raw position when no row exists', async () => {
    // Content outside the rows: a tall streaming block or a startup banner.
    const h = mountChat({ heights: [], scrollHeight: 5000 })
    await h.anchoredAt(1000)
    await h.preserved(h.reset)
    await h.restoreFrames()

    expect(h.div.getScrollTop()).toBe(1000)
    h.dispose()
  })

  it('writes nothing when the work did not move the view', async () => {
    const h = mountChat()
    await h.anchoredAt(2000)
    const writes = await h.preserved(() => {})
    await h.restoreFrames()

    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('writes nothing when the row under the reader grew during the work', async () => {
    const h = mountChat({
      heights: [
        ...Array.from<number>({ length: 40 }).fill(100),
        1000,
      ],
    })
    await h.anchoredAt(4200)
    const writes = await h.preserved(() => h.changeBelow(500))
    await h.restoreFrames()

    // A comparison of resolved positions would write: the row under the reader changed.
    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.div.getScrollTop()).toBe(4200)
    h.dispose()
  })

  it('writes nothing after a rebase during the work', async () => {
    const h = mountChat()
    await h.anchoredAt(2000)
    const writes = await h.preserved(() => h.changeAbove(60))
    expect(h.rebaseCount()).toBe(1)
    await h.restoreFrames()

    // A comparison with the scrollTop of the snapshot would write.
    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.div.getScrollTop()).toBe(2060)
    h.dispose()
  })

  it('writes nothing for a follower that stayed at the bottom', async () => {
    const h = mountChat()
    const writes = await h.preserved(() => h.growTailPinned(200))
    await h.restoreFrames()

    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })
})
