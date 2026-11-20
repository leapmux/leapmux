import type { ChatScrollState, ChatScrollVirtualizer } from './useChatScroll'
import type { ScrollHarnessOptions } from './useChatScroll.testkit'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { batch, createEffect, createRoot, createSignal, onCleanup } from 'solid-js'
import { insert } from 'solid-js/web'
import { describe, expect, it, vi } from 'vitest'
import { inRoot } from '~/test-support/async'
import {
  triggerResizeObserversSync,
} from '~/test-support/resizeObserverStub'
import { useChatScroll } from './useChatScroll'
import { deferredJump, installScrollTestEnv, makeFakeScrollDiv, makeRowVirtualizer, makeStubVirtualizer, mountScrollHarness, rowMessages, rowTargetAt, settleJump, withStaticAdmission } from './useChatScroll.testkit'

installScrollTestEnv()

describe('useChatScroll viewport restore', () => {
  it('jumps to the live tail on restore when the saved anchor was trimmed away', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(0) // hidden tab
      const [messages] = createSignal<AgentChatMessage[]>([])
      // Saved while scrolled away from the tail; the anchored row (id m42)
      // was trimmed out of the window while hidden, so the stub's
      // scrollTopForAnchor returns null (the anchor no longer resolves).
      const [saved] = createSignal<ChatScrollState | undefined>({
        anchor: { id: 'm42', offsetWithinRow: 10 },
        atBottom: false,
        hasMoreNewer: true,
      })
      let jumped = false
      let cleared = false
      const onJumpToLatest = () => {
        jumped = true
        return Promise.resolve(true)
      }
      const onClearSavedViewportScroll = () => {
        cleared = true
      }
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: makeStubVirtualizer(),
        messages,
        hasNewerMessages: () => true,
        onJumpToLatest,
        savedViewportScroll: saved,
        onClearSavedViewportScroll,
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Tab becomes visible: the hidden->visible transition runs the
      // restore path. The anchor can't resolve and we were windowed away,
      // so it jumps to the live tail instead of clamping to the top.
      div.setClientHeight(500)
      triggerResizeObserversSync()
      await Promise.resolve()
      await Promise.resolve()

      expect(jumped).toBe(true)
      expect(cleared).toBe(true)
    }))

  it('jumps to the live tail on restore when the saved at-bottom view was windowed away', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(0) // hidden tab
      const [messages] = createSignal<AgentChatMessage[]>([])
      // Saved at the IN-MEMORY bottom while still windowed away from the live
      // tail (hasMoreNewer): the in-memory bottom is NOT the real bottom, so
      // restore must re-fetch the tail rather than stick to the stale window.
      const [saved] = createSignal<ChatScrollState | undefined>({
        atBottom: true,
        hasMoreNewer: true,
      })
      let jumped = false
      let cleared = false
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: makeStubVirtualizer(),
        messages,
        hasNewerMessages: () => true,
        onJumpToLatest: () => {
          jumped = true
          return Promise.resolve(true)
        },
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => { cleared = true },
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Tab becomes visible: the hidden->visible restore path runs.
      div.setClientHeight(500)
      triggerResizeObserversSync()
      await Promise.resolve()
      await Promise.resolve()

      // forceScrollToBottom jumps to the live tail (not a plain stickToBottom).
      expect(jumped).toBe(true)
      expect(cleared).toBe(true)
    }))

  it('preserves the landing allowance when a saved tail gap has already closed', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(300)
      div.setClientHeight(0)
      const onSetLandingRefillPages = vi.fn()
      const onLoadOlderMessages = vi.fn()
      const [saved] = createSignal<ChatScrollState | undefined>({ atBottom: true, hasMoreNewer: true })
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: makeStubVirtualizer(),
        messages: () => [],
        savedViewportScroll: saved,
        landingRefillPages: () => 0,
        onSetLandingRefillPages,
        hasOlderMessages: () => true,
        hasNewerMessages: () => false,
        onLoadOlderMessages,
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      div.setClientHeight(500)
      triggerResizeObserversSync()
      await Promise.resolve()
      await Promise.resolve()

      expect(onSetLandingRefillPages).not.toHaveBeenCalled()
      expect(onLoadOlderMessages).not.toHaveBeenCalled()
      expect(hook.readHistoryIntent()).toBe('tail')
    }))

  it('restores a saved raw scrollTop fallback when no row anchor resolves', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(0) // hidden tab
      const [messages] = createSignal<AgentChatMessage[]>([])
      // Saved while scrolled up in an all-hidden window: no anchor, but a raw
      // scrollTop fallback (no virtual spacer there, so no drift to fear).
      const [saved] = createSignal<ChatScrollState | undefined>({
        rawScrollTop: 1200,
        atBottom: false,
        hasMoreNewer: false,
      })
      const hook = useChatScroll(withStaticAdmission({
        // anchorAt / scrollTopForAnchor return null -> the anchor never resolves.
        virtualizer: makeStubVirtualizer(),
        messages,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => {},
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Tab becomes visible: restore falls back to the raw scrollTop (clamped to
      // scrollHeight - clientHeight = 4500), not a snap to the live tail.
      div.setClientHeight(500)
      triggerResizeObserversSync()
      await Promise.resolve()
      await Promise.resolve()
      expect(div.getScrollTop()).toBe(1200)
      expect(hook.atBottom()).toBe(false)
    }))

  it('discards a stale raw scrollTop and clamps to the top when virtual rows appeared between save and restore', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(0) // hidden tab
      const [messages] = createSignal<AgentChatMessage[]>([])
      // Saved while all-hidden (raw fallback, no anchor). But by restore time
      // the window has virtual content: totalHeight > 0 means a spacer now sits
      // where the raw offset pointed, so 1200 no longer maps to that content.
      const [saved] = createSignal<ChatScrollState | undefined>({
        rawScrollTop: 1200,
        atBottom: false,
        hasMoreNewer: false,
      })
      let olderLoads = 0
      // The stub's anchorAt returns null: the all-hidden save carried no anchor.
      const virt: ChatScrollVirtualizer = {
        ...makeStubVirtualizer(),
        totalHeight: () => 3000, // virtual rows exist now
      }
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => {},
        hasOlderMessages: () => true,
        onLoadOlderMessages: () => { olderLoads++ },
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Tab becomes visible: the stale raw 1200 is discarded; restore clamps to
      // the top (best-effort, since there's no anchor to resolve) instead of
      // landing on the wrong rows.
      div.setClientHeight(500)
      triggerResizeObserversSync()
      await Promise.resolve()
      await Promise.resolve()
      expect(div.getScrollTop()).toBe(0)
      expect(hook.atBottom()).toBe(false)
      // The restore itself pages no older history: the buffer fill runs on a
      // scroll event or a message change, and neither follows the restore here.
      expect(olderLoads).toBe(0)
    }))
})

describe('useChatScroll restore under a layout shift', () => {
  it('loads older history after a restore near the top', async () => {
    const onLoadOlderMessages = vi.fn()
    const [saved, setSaved] = createSignal<ChatScrollState | undefined>({
      anchor: { id: 'r1', offsetWithinRow: 0 },
      atBottom: false,
      hasMoreNewer: false,
    })
    const h = mountScrollHarness({
      clientHeight: 0,
      hookOptions: {
        hasOlderMessages: () => true,
        onLoadOlderMessages,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => setSaved(undefined),
      },
    })
    h.div.setClientHeight(500)
    await h.resizeFlush()
    expect(h.div.getScrollTop()).toBe(100)
    // The scroll event of the restore write.
    h.hook.handlers.onScroll()

    expect(onLoadOlderMessages).toHaveBeenCalledOnce()
    h.dispose()
  })

  it('restores a hidden reader under a shift', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.changeAbove(300)
    expect(h.virt.layoutShift()).toBe(-300)

    await h.hideThenShow()

    expect(h.virt.layoutShift()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2290)
    h.dispose()
  })

  it('restores the row when the position changed while the pane was hidden', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.changeAbove(300)
    h.div.setClientHeight(0)
    await h.resizeFlush()
    h.div.setScrollTop(0)
    h.div.setClientHeight(500)
    await h.resizeFlush()

    expect(h.virt.layoutShift()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2290)
    h.dispose()
  })

  it('restores the row when a scroll event of the browser arrives before the flush', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.div.setClientHeight(0)
    await h.resizeFlush()
    h.div.setClientHeight(500)
    triggerResizeObserversSync()
    h.div.setScrollTop(0)
    h.hook.handlers.onScroll()
    await h.frame()

    // A restore that reads the anchor at the flush leaves 0.
    expect(h.div.getScrollTop()).toBe(1990)
    h.dispose()
  })

  it('restores the anchor of a reader who left at the loaded bottom of a paged-away window', async () => {
    const onJumpToLatest = vi.fn()
    const [saved, setSaved] = createSignal<ChatScrollState | undefined>()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: {
        onJumpToLatest,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => setSaved(undefined),
      },
    })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    setSaved(h.hook.getScrollState())

    await h.hideThenShow()

    // A save that records the position as at-bottom makes the show jump to the latest page.
    expect(onJumpToLatest).not.toHaveBeenCalled()
    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('keeps the anchor when rows change between the show and its flush', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.div.setClientHeight(0)
    h.div.setScrollTop(0)
    await h.resizeFlush()
    h.div.setClientHeight(500)
    // The geometry effect runs its position check at the position that the hook did not see.
    h.changeBelow(100)
    await h.resizeFlush()

    expect(h.div.getScrollTop()).toBe(1990)
    h.dispose()
  })
})

describe('useChatScroll restore inside a row', () => {
  // The rows are 100px high, so 1250 lies 50px into row 12 (seq 13). A resolve by the seq
  // alone lands on the top of the row, 1200.
  it('returns a hidden reader to the position inside the row', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(1250)
    h.div.setClientHeight(0)
    await h.resizeFlush()
    h.div.setScrollTop(0)
    h.div.setClientHeight(500)
    await h.resizeFlush()

    expect(h.div.getScrollTop()).toBe(1250)
    h.dispose()
  })
})

// Row 0 has a measured height of 1000px that no read of its mounted copy confirmed: a
// hidden copy measured it, or a fling skeleton stands in for the row. The 40 rows below it
// measure 100px each. A restore places a reader halfway into row 0, at 500. The first read
// of row 0 then gives 600px, and the rule holds the bottom edge of a row whose height the
// reader did not see: the view shows row 0 at 100px. The hold places the view on the anchor
// again, halfway into the row: at 300.
describe('useChatScroll restore onto a row that does not show its height yet', () => {
  const heights = [
    1000,
    ...Array.from<number>({ length: 40 }).fill(100),
  ]

  /** A reader halfway into row 0 whose pane was hidden and shown: the restore holds the anchor. */
  async function restoredHalfwayIntoRow0(opts: ScrollHarnessOptions = {}) {
    const h = mountScrollHarness({
      heights,
      unseenRows: [0],
      ...opts,
    })
    await h.anchoredAt(500)
    await h.hideThenShow()
    expect(h.div.getScrollTop()).toBe(500)
    return h
  }

  it('lands again on the anchor at the first read of its row after a show', async () => {
    const h = await restoredHalfwayIntoRow0()

    h.changeAbove(-400)

    expect(h.div.getScrollTop()).toBe(300)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('lands again on a saved anchor at the first read of its row after a mount', () => {
    const h = mountScrollHarness({
      heights,
      unseenRows: [0],
      hookOptions: {
        savedViewportScroll: () => ({
          anchor: {
            id: 'r0',
            offsetWithinRow: 500,
            basisHeight: 1000,
            seq: 1n,
          },
          atBottom: false,
          hasMoreNewer: false,
        }),
      },
    })
    expect(h.div.getScrollTop()).toBe(500)

    h.changeAbove(-400)

    expect(h.div.getScrollTop()).toBe(300)
    h.dispose()
  })

  it('keeps the hold while another row changes, and the row that the reader did not see keeps its height', async () => {
    const h = await restoredHalfwayIntoRow0()

    // The last row grows below the reader. Row 0 has no read yet, so the hold stays, and
    // its first read then lands the view on the anchor.
    h.changeBelow(50)
    expect(h.div.getScrollTop()).toBe(500)
    h.changeAbove(-400)

    expect(h.div.getScrollTop()).toBe(300)
    h.dispose()
  })

  it('ends the hold at a move of the reader', async () => {
    const h = await restoredHalfwayIntoRow0()
    h.scrollTo(520)

    h.changeAbove(-400)

    // The shift keeps the content under the reader, and nothing places the view again.
    expect(h.div.getScrollTop()).toBe(520)
    expect(h.virt.layoutShift()).toBe(400)
    h.dispose()
  })

  it('ends the hold at a move whose scroll event did not arrive yet', async () => {
    const h = await restoredHalfwayIntoRow0()
    // A touch or scrollbar drag moves the view, and its event is late.
    h.div.setScrollTop(520)

    h.changeAbove(-400)

    // A placement would undo the move and record it as seen.
    expect(h.div.getScrollTop()).toBe(520)
    expect(h.virt.layoutShift()).toBe(400)
    h.dispose()
  })

  it('reports the placement of a restore to the virtualizer, and no placement for a page key', async () => {
    const h = await restoredHalfwayIntoRow0()
    // The show placed the view on the anchor of the reader.
    expect(h.notePlacement).toHaveBeenLastCalledWith(500)
    h.notePlacement.mockClear()

    // A page moves the reader along the content, as a scroll does.
    h.hook.pageScroll(1)

    expect(h.div.getScrollTop()).toBe(952)
    expect(h.notePlacement).not.toHaveBeenCalled()
    h.dispose()
  })

  it('reports the placement to the virtualizer when the view lands again on a held anchor', async () => {
    const h = await restoredHalfwayIntoRow0()
    h.notePlacement.mockClear()

    // The row of the anchor measures, and the view lands on the anchor again.
    h.changeAbove(-400)

    expect(h.div.getScrollTop()).toBe(300)
    expect(h.notePlacement).toHaveBeenCalledTimes(1)
    expect(h.notePlacement).toHaveBeenLastCalledWith(300)
    h.dispose()
  })

  it('ends the hold at a page key', async () => {
    const h = await restoredHalfwayIntoRow0()
    h.hook.pageScroll(1)
    expect(h.div.getScrollTop()).toBe(952)

    h.changeAbove(-400)

    // The rebase at rest moves the shift of 400px into the position of the page.
    expect(h.div.getScrollTop()).toBe(552)
    expect(h.virt.layoutShift()).toBe(0)
    h.dispose()
  })

  it('ends the hold when the reader requests the tail', async () => {
    const jump = deferredJump()
    const h = await restoredHalfwayIntoRow0({
      hasNewer: true,
      hookOptions: { onJumpToLatest: jump.onJumpToLatest },
    })
    // End requests the latest page, and row 0 measures while the page loads.
    h.hook.forceScrollToBottom()
    h.changeAbove(-400)
    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    // A placement on the anchor would end the request, and the view would stay in row 0.
    expect(h.hook.isFollowing()).toBe(true)
    expect(h.div.getScrollTop()).toBe(h.div.el.scrollHeight - h.div.el.clientHeight)
    h.dispose()
  })
})

describe('useChatScroll resize', () => {
  it('uses one landing refill when a settled resize makes the page unscrollable', async () => {
    const loadNewer = vi.fn()
    const [remaining, setRemaining] = createSignal<0 | 1>(1)
    const h = mountScrollHarness({
      heights: [100, 100, 100, 100, 100, 100],
      hasNewer: true,
      hookOptions: {
        landingRefillPages: remaining,
        onSetLandingRefillPages: pages => setRemaining(pages ?? 0),
        onLoadNewerMessages: loadNewer,
      },
    })
    expect(loadNewer).not.toHaveBeenCalled()
    h.div.setClientHeight(700)
    await h.resizeFlush()
    expect(loadNewer).toHaveBeenCalledExactlyOnceWith('landing-refill')
    await h.resizeFlush()
    expect(loadNewer).toHaveBeenCalledOnce()
    h.dispose()
  })

  it('reports a native move away from the tail before its scroll event', () => {
    const h = mountScrollHarness()
    expect(h.hook.readHistoryIntent()).toBe('tail')
    h.div.setScrollTop(4000)
    expect(h.hook.readHistoryIntent()).toBe('history')
    h.dispose()
  })

  it('does not stick when the reader leaves follow mode between a resize and its flush', async () => {
    const h = mountScrollHarness()
    expect(h.hook.isFollowing()).toBe(true)
    // The pane grows to 600px: the browser clamps scrollTop to 4400, and the
    // ResizeObserver delivers before the next frame.
    h.div.setClientHeight(600)
    triggerResizeObserversSync()
    // The reader scrolls up before that frame.
    h.scrollTo(4000)
    expect(h.hook.isFollowing()).toBe(false)

    await h.frame()

    // A flush that applies the request of the ResizeObserver callback sticks to 4400.
    expect(h.div.getScrollTop()).toBe(4000)
    expect(h.hook.isFollowing()).toBe(false)
    h.dispose()
  })

  it('renders the rows for the new height after a resize of a reader who does not follow', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.updateViewport.mockClear()

    h.div.setClientHeight(600)
    await h.resizeFlush()

    // The taller pane shows rows below the old slice, and no scroll event follows.
    expect(h.div.getScrollTop()).toBe(2000)
    expect(h.updateViewport).toHaveBeenLastCalledWith(2000, 600, undefined)
    h.dispose()
  })

  it('sticks a follower after a resize', async () => {
    const h = mountScrollHarness()
    h.div.setClientHeight(400)
    await h.resizeFlush()

    expect(h.div.getScrollTop()).toBe(4600)
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })
})

describe('useChatScroll unmount save + visible-mount restore', () => {
  // A tile split/merge or workspace switch REMOUNTS ChatView over a still-populated
  // store: the old instance saves its final viewport state from onCleanup
  // (onSaveViewportScroll) and the fresh instance -- which mounts VISIBLE, so the
  // hidden->visible RO path can never fire -- restores it at mount (restoreOnMount),
  // beating the default tail-stick placement.
  it('round-trips a scrolled-up reading position across unmount and visible remount', async () => {
    let savedState: ChatScrollState | undefined
    await inRoot(async (disposeA) => {
      const divA = makeFakeScrollDiv()
      divA.setClientHeight(500)
      divA.setScrollHeight(3000)
      const { virt: virtA } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
      const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      const hookA = useChatScroll(withStaticAdmission({
        virtualizer: virtA,
        messages,
        onSaveViewportScroll: (state) => { savedState = state },
      }))
      hookA.attachListRef(divA.el)
      // Mount placement sticks the populated fresh mount to the tail...
      await Promise.resolve()
      expect(divA.getScrollTop()).toBe(2500)
      // ...then the reader scrolls up into history (a genuine scroll event), to 250px
      // inside row 2. A resolve by the seq alone loses that offset and lands at 1000.
      divA.setScrollTop(1250)
      hookA.handlers.onScroll()
      disposeA()

      // The unmount saved the final position, anchored to the viewport-top row.
      expect(savedState).toBeDefined()
      expect(savedState!.atBottom).toBe(false)
      expect(savedState!.anchor?.id).toBe('g0_2')
    })

    await inRoot(async () => {
      // The remount: a FRESH container at scrollTop 0 over the same window
      // (same row ids -> the saved anchor resolves against the new offset map).
      const divB = makeFakeScrollDiv()
      divB.setClientHeight(500)
      divB.setScrollHeight(3000)
      const { virt: virtB } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
      const [messagesB] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      let cleared = false
      const hookB = useChatScroll(withStaticAdmission({
        virtualizer: virtB,
        messages: messagesB,
        savedViewportScroll: () => savedState,
        onClearSavedViewportScroll: () => { cleared = true },
      }))
      hookB.attachListRef(divB.el)
      await Promise.resolve()
      await Promise.resolve()

      // Restored to the saved row -- NOT the default tail stick -- and consumed.
      expect(divB.getScrollTop()).toBe(1250)
      expect(hookB.atBottom()).toBe(false)
      expect(cleared).toBe(true)
    })
  })

  it('does not save on unmount when the pane is hidden (a tab-switch save must survive)', () =>
    inRoot(async (dispose) => {
      const div = makeFakeScrollDiv()
      div.setClientHeight(0) // hidden tab
      div.setScrollHeight(3000)
      const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      let saves = 0
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: makeStubVirtualizer(),
        messages,
        onSaveViewportScroll: () => { saves++ },
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      dispose()

      // An unmeasurable pane carries no position: the host's existing saved
      // entry (written when the tab was switched away) must not be clobbered.
      expect(saves).toBe(0)
    }))

  it('does not read the host pagination accessor while its owner disposes', () => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(500)
    div.setScrollHeight(3000)
    const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
    let disposalStarted = false
    let reentered = false
    let savedState: ChatScrollState | undefined
    let disposeRoot!: () => void

    createRoot((dispose) => {
      disposeRoot = dispose
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: makeStubVirtualizer(),
        messages,
        hasNewerMessages: () => {
          if (disposalStarted && !reentered) {
            reentered = true
            dispose()
          }
          return true
        },
        onSaveViewportScroll: (state) => { savedState = state },
      }))
      hook.attachListRef(div.el)
    })

    disposalStarted = true
    expect(() => disposeRoot()).not.toThrow()
    expect(reentered).toBe(false)
    // A mount on a window that is paged away and never scrolled holds no request for the tail.
    expect(savedState).toMatchObject({ atBottom: false, hasMoreNewer: true })
  })

  it('consumes an at-bottom save on a visible mount (stick + clear)', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setClientHeight(500)
      div.setScrollHeight(3000)
      const { virt } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
      const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      const [saved] = createSignal<ChatScrollState | undefined>({ atBottom: true, hasMoreNewer: false })
      let cleared = false
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => { cleared = true },
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()

      expect(div.getScrollTop()).toBe(2500)
      expect(hook.atBottom()).toBe(true)
      // The mount restore CONSUMED the save (vs the default placement, which
      // would leave it lingering for a later hidden->visible to misapply).
      expect(cleared).toBe(true)
    }))

  it('jumps to the live tail on a visible mount of an at-bottom save that was windowed away', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setClientHeight(500)
      div.setScrollHeight(3000)
      const { virt } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
      const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      const [saved] = createSignal<ChatScrollState | undefined>({ atBottom: true, hasMoreNewer: true })
      let jumped = false
      let cleared = false
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        hasNewerMessages: () => true,
        onJumpToLatest: () => {
          jumped = true
          return Promise.resolve(true)
        },
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => { cleared = true },
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // The saved in-memory bottom is NOT the live tail: re-fetch the tail
      // (forceScrollToBottom jumps when hasNewerMessages) instead of sticking
      // to the stale loaded bottom.
      expect(jumped).toBe(true)
      expect(cleared).toBe(true)
    }))

  it('leaves a saved state for the hidden->visible path when the mount is hidden', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setClientHeight(0) // hidden tab: the RO hidden->visible path owns the restore
      div.setScrollHeight(3000)
      const { virt } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
      const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      const [saved] = createSignal<ChatScrollState | undefined>({
        anchor: { id: 'g0_2', seq: 3n, offsetWithinRow: 0 },
        atBottom: false,
        hasMoreNewer: false,
      })
      let cleared = false
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => { cleared = true },
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Not consumed at mount; the tab-switch (hidden->visible) restore -- covered
      // by the suite above -- picks it up when the pane becomes visible.
      expect(cleared).toBe(false)
      expect(div.getScrollTop()).toBe(0)
    }))

  it('framework contract: onCleanup runs while the unmounting DOM is still connected', () => {
    // The unmount save reads clientHeight/scrollTop from onCleanup, which only works
    // if Solid disposes owners BEFORE detaching their DOM. Pin that contract: if a
    // Solid upgrade ever flips the order, the save would silently degrade to a no-op
    // (clientHeight 0 -> getScrollState undefined), and this test names the cause.
    const host = document.createElement('div')
    document.body.appendChild(host)
    try {
      const [show, setShow] = createSignal(true)
      let connectedAtCleanup: boolean | undefined
      const Inner = () => {
        const el = document.createElement('div')
        el.textContent = 'content'
        onCleanup(() => {
          connectedAtCleanup = el.isConnected
        })
        return el
      }
      // The same reactive insert a compiled <Show>/component boundary lowers to:
      // toggling the accessor disposes the branch owner, then swaps the DOM. The
      // toggle happens OUTSIDE the root body -- createRoot batches writes made
      // inside it, deferring the re-run past these assertions.
      const dispose = createRoot((d) => {
        insert(host, () => (show()
          ? Inner()
          : null))
        return d
      })
      expect(host.textContent).toContain('content')
      setShow(false)
      expect(connectedAtCleanup).toBe(true)
      expect(host.textContent).not.toContain('content')
      dispose()
    }
    finally {
      host.remove()
    }
  })
})

// The rows of a place can be staged: loaded, and not measured yet. A restore, the first
// view, and a command then wait, and the view moves once, when the rows are reached.
describe('useChatScroll a place that waits for its rows', () => {
  // The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500, and row `r20`
  // starts at 2000. The target of each request waits until the test reaches it.
  const SAVED_AT_R20: ChatScrollState = {
    anchor: { id: 'r20', offsetWithinRow: 0 },
    atBottom: false,
    hasMoreNewer: false,
  }

  /** A mount whose targets wait, with a spy on the save of the unmount, and a saved state when `saved` gives one. */
  function mountWaiting(opts: ScrollHarnessOptions & { saved?: ChatScrollState } = {}) {
    const { saved, ...harnessOptions } = opts
    const onSaveViewportScroll = vi.fn()
    const onClearSavedViewportScroll = vi.fn()
    const h = mountScrollHarness({
      holdTargets: true,
      ...harnessOptions,
      hookOptions: {
        onSaveViewportScroll,
        onClearSavedViewportScroll,
        ...(saved === undefined
          ? {}
          : {
              savedViewportScroll: () => saved,
            }),
        ...harnessOptions.hookOptions,
      },
    })
    /** The state that the unmount saved. */
    const savedAtUnmount = (): ChatScrollState => {
      expect(onSaveViewportScroll).toHaveBeenCalledOnce()
      return onSaveViewportScroll.mock.calls[0]![0] as ChatScrollState
    }
    return { h, onClearSavedViewportScroll, savedAtUnmount }
  }

  describe('a restore of a saved viewport', () => {
    it('asks for the top of the loaded row nearest a saved anchor whose row left the loaded rows', () => {
      const jump = deferredJump()
      const { h } = mountWaiting({
        hasNewer: true,
        // The row of the saved anchor was trimmed away, and `r3` is the nearest loaded row.
        loadedRows: {
          has: id => id !== 'r20',
          nearestId: () => 'r3',
        },
        saved: {
          anchor: { id: 'r20', seq: 21n, offsetWithinRow: 40 },
          atBottom: false,
          hasMoreNewer: true,
        },
        hookOptions: { onJumpToLatest: jump.onJumpToLatest },
      })

      // The place keeps the seq of the saved anchor, and it takes no offset of the row that
      // left: that offset means nothing inside another row.
      expect(h.requestedTargets()).toEqual([
        {
          kind: 'row',
          anchor: { id: 'r3', seq: 21n, offsetWithinRow: 0 },
        },
      ])
      // The restore found a row, so it starts no jump to the live tail.
      expect(jump.onJumpToLatest).not.toHaveBeenCalled()

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(300)
      h.dispose()
    })

    it('keeps the view until the row of the saved anchor is reached', () => {
      const { h } = mountWaiting({ saved: SAVED_AT_R20 })

      expect(h.div.getScrollTop()).toBe(0)
      expect(h.hook.isFollowing()).toBe(false)
      expect(h.requestedTargets()).toEqual([
        rowTargetAt('r20'),
      ])

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(2000)
      h.dispose()
    })

    it('saves the saved anchor again at an unmount during the wait', () => {
      const { h, savedAtUnmount } = mountWaiting({ saved: SAVED_AT_R20 })

      h.dispose()

      const saved = savedAtUnmount()
      expect(saved).toMatchObject({
        anchor: { id: 'r20', offsetWithinRow: 0 },
        atBottom: false,
      })
      // The view shows no position of its own, so no raw position is saved.
      expect(saved).not.toHaveProperty('rawScrollTop')
    })

    it('asks for the row of a saved anchor in a window with newer pages, and starts no jump', () => {
      const jump = deferredJump()
      const { h } = mountWaiting({
        hasNewer: true,
        saved: {
          anchor: { id: 'r0', offsetWithinRow: 0 },
          atBottom: false,
          hasMoreNewer: true,
        },
        hookOptions: { onJumpToLatest: jump.onJumpToLatest },
      })

      expect(h.requestedTargets()).toEqual([
        rowTargetAt('r0'),
      ])
      expect(jump.onJumpToLatest).not.toHaveBeenCalled()
      h.dispose()
    })
  })

  describe('an unmount during the wait of a command', () => {
    it('saves the row of a seek', () => {
      const { h, savedAtUnmount } = mountWaiting({
        messages: rowMessages(1),
      })
      void h.hook.jumpToSeq(21n)

      h.dispose()

      expect(savedAtUnmount()).toMatchObject({
        anchor: { id: 'r20', seq: 21n, offsetWithinRow: 0 },
        atBottom: false,
      })
    })

    it('saves the request for the tail of End', async () => {
      const { h, savedAtUnmount } = mountWaiting({ stagedNewer: true })
      await h.anchoredAt(2000)
      h.press('End')

      h.dispose()

      expect(savedAtUnmount()).toMatchObject({ atBottom: true })
    })

    it('saves the first row for Home', () => {
      const { h, savedAtUnmount } = mountWaiting()
      h.press('Home')

      h.dispose()

      expect(savedAtUnmount()).toMatchObject({
        anchor: { id: 'r0', offsetWithinRow: 0 },
        atBottom: false,
      })
    })
  })

  describe('the first view', () => {
    it('asks for the tail for a follower, and sticks nothing before the rows are reached', () => {
      const { h } = mountWaiting({ awaitingFirstView: true })

      expect(h.requestedTargets()).toEqual([
        { kind: 'tail' },
      ])
      expect(h.div.getScrollTop()).toBe(0)

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      expect(h.hook.isFollowing()).toBe(true)
      h.dispose()
    })

    it('asks for no second place beside the restore of a saved anchor', () => {
      const { h } = mountWaiting({
        awaitingFirstView: true,
        saved: {
          ...SAVED_AT_R20,
          anchor: { id: 'r20', offsetWithinRow: 30 },
        },
      })

      expect(h.requestedTargets()).toEqual([
        rowTargetAt('r20', 30),
      ])
      h.dispose()
    })

    it('asks for the tail once for a saved state at the bottom', () => {
      const { h, onClearSavedViewportScroll } = mountWaiting({
        awaitingFirstView: true,
        saved: { atBottom: true, hasMoreNewer: false },
      })

      expect(h.requestedTargets()).toEqual([
        { kind: 'tail' },
      ])
      expect(onClearSavedViewportScroll).toHaveBeenCalledOnce()

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      expect(h.hook.isFollowing()).toBe(true)
      h.dispose()
    })

    it('asks for the top in a window with newer pages and no saved state', () => {
      // The reader requests no tail there, and no anchor exists.
      const { h } = mountWaiting({ awaitingFirstView: true, hasNewer: true })

      expect(h.requestedTargets()).toEqual([
        { kind: 'top' },
      ])
      h.dispose()
    })

    it('asks for nothing while the jump to the latest page of the mount is pending, and for the tail when it settled', async () => {
      const jump = deferredJump()
      const { h } = mountWaiting({
        awaitingFirstView: true,
        hasNewer: true,
        saved: { atBottom: true, hasMoreNewer: true },
        hookOptions: { onJumpToLatest: jump.onJumpToLatest },
      })
      // The page of the jump replaces the rows, so their bottom is no first view.
      expect(jump.onJumpToLatest).toHaveBeenCalledOnce()
      expect(h.requestedTargets()).toEqual([])

      h.setHasNewer(false)
      jump.resolve(true)
      await settleJump(jump.promise)
      expect(h.requestedTargets()).toEqual([
        { kind: 'tail' },
      ])

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      expect(h.hook.isFollowing()).toBe(true)
      h.dispose()
    })

    it('asks for the loaded row nearest the last anchor when every row is replaced under a reader who scrolled up', async () => {
      const { h } = mountWaiting({
        // The row of the last anchor left the loaded rows, and `r3` is the nearest one.
        loadedRows: {
          has: id => id !== 'r20',
          nearestId: () => 'r3',
        },
      })
      await h.anchoredAt(2000)

      h.setAwaitingFirstView(true)

      expect(h.requestedTargets().at(-1)).toEqual(rowTargetAt('r3'))
      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(300)
      h.dispose()
    })

    it('records no position while loaded rows exist and none is in the list', async () => {
      const { h } = mountWaiting()
      await h.anchoredAt(2000)

      // One update: the rows are replaced, and the browser moves the view of the empty list.
      batch(() => {
        h.setAwaitingFirstView(true)
        h.scrollTo(3000)
      })

      // The place is the row of the last anchor, not the row at the position of the event.
      expect(h.requestedTargets().at(-1)).toEqual(rowTargetAt('r20'))
      h.dispose()
    })
  })

  describe('a row place that waits at the bottom', () => {
    it('follows no tail, and saves the anchor at an unmount', () => {
      const { h, savedAtUnmount } = mountWaiting({
        scrollTopBeforeMount: 4500,
        awaitingFirstView: true,
        saved: SAVED_AT_R20,
      })

      h.changeBelow(300)
      expect(h.hook.isFollowing()).toBe(false)

      h.dispose()
      expect(savedAtUnmount()).toMatchObject({
        anchor: { id: 'r20', offsetWithinRow: 0 },
        atBottom: false,
      })
    })

    it('follows no tail when the newer pages end during the wait', () => {
      const { h, savedAtUnmount } = mountWaiting({
        scrollTopBeforeMount: 4500,
        awaitingFirstView: true,
        hasNewer: true,
        saved: SAVED_AT_R20,
      })
      h.hook.handlers.onScroll()
      expect(h.hook.atBottom()).toBe(true)

      h.setHasNewer(false)

      expect(h.hook.isFollowing()).toBe(false)
      h.dispose()
      expect(savedAtUnmount()).toMatchObject({
        anchor: { id: 'r20', offsetWithinRow: 0 },
        atBottom: false,
      })
    })
  })

  describe('a pane with no layout box', () => {
    it('asks for the tail again at the show of a pane whose tail was reached while it was hidden', async () => {
      const { h } = mountWaiting({ awaitingFirstView: true })
      h.div.setClientHeight(0)
      await h.resizeFlush()

      const writes = h.div.scrollTopWrites()
      h.reachTarget()
      expect(h.div.scrollTopWrites()).toBe(writes)

      h.div.setClientHeight(500)
      await h.resizeFlush()
      // The show asks for the tail again: rows that arrived meanwhile are staged.
      expect(h.requestedTargets()).toEqual([
        { kind: 'tail' },
        { kind: 'tail' },
      ])

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      expect(h.hook.isFollowing()).toBe(true)
      h.dispose()
    })

    it('restores the saved anchor of a hidden mount when a tail was reached before the flush of the show', async () => {
      const { h } = mountWaiting({
        clientHeight: 0,
        awaitingFirstView: true,
        saved: SAVED_AT_R20,
      })
      // A hidden mount restores nothing, so the first-view rule asked for the tail.
      expect(h.requestedTargets()).toEqual([
        { kind: 'tail' },
      ])

      // The browser gives the pane its layout box one frame before the flush of the show.
      h.div.setClientHeight(500)
      triggerResizeObserversSync()
      const writes = h.div.scrollTopWrites()
      h.reachTarget()
      // No stick: the flush holds the saved viewport, and it decides first.
      expect(h.div.scrollTopWrites()).toBe(writes)

      await h.frame()
      expect(h.requestedTargets().at(-1)).toEqual(rowTargetAt('r20'))

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(2000)
      h.dispose()
    })

    it('waits for the row of the last anchor at the show of a pane', async () => {
      const { h } = mountWaiting()
      await h.anchoredAt(2000)
      h.div.setClientHeight(0)
      await h.resizeFlush()
      h.div.setScrollTop(0)

      h.div.setClientHeight(500)
      await h.resizeFlush()

      // The row of the anchor can be staged since the hide, so the show asks for the place
      // of the anchor and waits for its row, as the restore of a saved viewport does.
      expect(h.requestedTargets().at(-1)).toEqual(rowTargetAt('r20', 0))

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(2000)
      h.dispose()
    })

    it('keeps a wait for the top at the show of a pane whose saved viewport is older', async () => {
      const jump = deferredJump()
      const [saved, setSaved] = createSignal<ChatScrollState | undefined>()
      const { h } = mountWaiting({
        hookOptions: {
          hasOlderMessages: () => true,
          onJumpToOldest: jump.onJumpToOldest,
          savedViewportScroll: saved,
          onClearSavedViewportScroll: () => setSaved(undefined),
        },
      })
      h.press('Home')
      // A tab switch saves the viewport while the jump runs: the reader place is still the tail.
      setSaved(h.hook.getScrollState())
      h.div.setClientHeight(0)
      await h.resizeFlush()
      jump.resolve(true)
      await settleJump(jump.promise)
      expect(h.requestedTargets().at(-1)).toEqual({ kind: 'top' })

      h.div.setClientHeight(500)
      await h.resizeFlush()

      // The wait for the top is newer than the saved viewport, so the show keeps it.
      expect(h.requestedTargets().at(-1)).toEqual({ kind: 'top' })
      expect(saved()).toBeUndefined()

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(0)
      h.dispose()
    })

    it('reports the pane as hidden until the flush of its show, to a reader that subscribes', async () => {
      const { h } = mountWaiting()
      const seen: boolean[] = []
      const disposeReader = createRoot((dispose) => {
        createEffect(() => seen.push(h.hook.paneHidden()))
        return dispose
      })
      h.div.setClientHeight(0)
      await h.resizeFlush()

      // The browser gives the pane its layout box one frame before the flush of the show.
      h.div.setClientHeight(500)
      triggerResizeObserversSync()
      expect(h.hook.paneHidden()).toBe(true)

      await h.frame()
      expect(seen).toEqual([false, true, false])
      disposeReader()
      h.dispose()
    })

    it('keeps a follower in follow mode at the show of a pane whose new messages are staged', async () => {
      const { h } = mountWaiting()
      expect(h.hook.isFollowing()).toBe(true)
      h.div.setClientHeight(0)
      await h.resizeFlush()
      h.setStagedNewer(true)

      h.div.setClientHeight(500)
      await h.resizeFlush()

      // The reader has the tail already: only new messages are staged below. So nothing
      // waits, and follow mode shows each message when it is measured.
      expect(h.requestedTargets()).toEqual([])
      expect(h.hook.isFollowing()).toBe(true)
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      h.dispose()
    })

    it('asks for the tail at the show of a pane whose staged rows are no new messages', async () => {
      const { h } = mountWaiting()
      expect(h.hook.isFollowing()).toBe(true)
      h.div.setClientHeight(0)
      await h.resizeFlush()
      // The width changed while the pane was hidden: the list showed these rows before.
      h.setStagedNewer(true)
      h.setHoldsTail(false)

      h.div.setClientHeight(500)
      await h.resizeFlush()
      expect(h.requestedTargets().at(-1)).toEqual({ kind: 'tail' })
      expect(h.hook.isFollowing()).toBe(false)

      h.reachTarget()
      expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
      expect(h.hook.isFollowing()).toBe(true)
      h.dispose()
    })
  })

  describe('viewAnchor', () => {
    it('gives the tail for a follower, and the row of the last anchor for a reader who scrolled up, also during a wait', async () => {
      const { h } = mountWaiting({
        messages: rowMessages(1),
      })
      expect(h.hook.viewAnchor()).toEqual({ kind: 'tail' })

      await h.anchoredAt(2000)
      expect(h.hook.viewAnchor()).toEqual({ kind: 'row', id: 'r20' })

      // The view still shows row 20 while the seek waits for row 30.
      void h.hook.jumpToSeq(31n)
      expect(h.hook.viewAnchor()).toEqual({ kind: 'row', id: 'r20' })
      h.dispose()
    })

    it('gives the row at the top of the view for a follower who seeks', () => {
      const { h } = mountWaiting({
        messages: rowMessages(1),
      })

      void h.hook.jumpToSeq(21n)

      // The reader follows no tail during the wait, and the view shows the rows from row 45.
      expect(h.hook.viewAnchor()).toEqual({ kind: 'row', id: 'r45' })
      h.dispose()
    })
  })
})
