import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '~/test-support/async'
import { placementScrollTop } from './createLayoutShift'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, mountScrollHarness, virtualizerNoOps, withContentAnchors } from './useChatScroll.testkit'

installScrollTestEnv()

function mkMsgs(seqs: number[]): AgentChatMessage[] {
  return seqs.map(s => ({
    id: `m${s}`,
    seq: BigInt(s),
    source: MessageSource.USER,
  } as AgentChatMessage))
}

/**
 * One message for each row of mountScrollHarness, with the ids of the rows: row `i` has the
 * id `r${i}` and the seq `firstSeq + i`.
 */
function rowMessages(firstSeq: number): AgentChatMessage[] {
  return Array.from({ length: 50 }, (_, i) => ({
    id: `r${i}`,
    seq: BigInt(firstSeq + i),
    source: MessageSource.USER,
  } as AgentChatMessage))
}

/**
 * A virtualizer stub that resolves an anchor to `Number(seq) * 200` scrollTop. The nearest
 * resolve gives the same, as the production one resolves the row itself first. A
 * `spacerInsetPx` puts the spacer that far below scroll coordinate 0, as the list's top
 * padding and row gap do: the conversions then differ by it.
 */
function seekVirt(spacerInsetPx = 0): ChatScrollVirtualizer {
  const resolve = (a: ScrollAnchor) => (a.seq !== undefined
    ? Number(a.seq) * 200
    : null)
  return withContentAnchors({
    ...virtualizerNoOps(),
    contentTopOf: scrollTop => scrollTop - spacerInsetPx,
    scrollTopOf: contentTop => contentTop + spacerInsetPx,
    placementScrollTopOf: contentTop => placementScrollTop(contentTop, top => top + spacerInsetPx),
    totalHeight: () => 5000,
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => ({ id: 'x', offsetWithinRow: 0 }),
    scrollTopNearAnchor: resolve,
    scrollTopForAnchor: resolve,
  })
}

describe('useChatScroll jump to seq', () => {
  it('lands on an in-window seq: writes the resolved row top minus the align offset', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(0)
      const [messages] = createSignal(mkMsgs([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]))
      const onJumpToSeq = vi.fn()
      const hook = useChatScroll({
        virtualizer: seekVirt(),
        messages,
        hasOlderMessages: () => false,
        hasNewerMessages: () => false,
        onJumpToSeq,
      })
      hook.attachListRef(div.el)

      hook.jumpToSeq(5n)

      // scrollTopForAnchor(seq 5) = 1000; landing subtracts the 8px align offset.
      expect(div.getScrollTop()).toBe(992)
      // In-window: no fetch.
      expect(onJumpToSeq).not.toHaveBeenCalled()
      dispose()
    }))

  it('resolves to whether the landing moved the view (the contract the rail drag-release hold relies on)', async () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal(mkMsgs([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            hasOlderMessages: () => false,
            hasNewerMessages: () => false,
          })
          hook.attachListRef(div.el)

          // From scrollTop 0, landing on seq 5 moves the view to 992 -> resolves true.
          expect(await hook.jumpToSeq(5n)).toBe(true)
          expect(div.getScrollTop()).toBe(992)
          // Landing on seq 5 AGAIN targets the same 992 -> no move -> resolves false. This is
          // the "scrolls nowhere" signal the rail's drag-release fallback keys off to clear the
          // held thumb instead of waiting forever for a metrics change that never comes.
          expect(await hook.jumpToSeq(5n)).toBe(false)
          expect(div.getScrollTop()).toBe(992)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('fetches around an out-of-window seq, then lands once the window swaps', async () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(2500)
          // The loaded window is seqs 100..110; the target (5) is far below it.
          const [messages, setMessages] = createSignal(mkMsgs([100, 101, 102, 103, 104]))
          const onJumpToSeq = vi.fn(async (_seq: bigint) => {
            // The paginator swaps the window to a page around the target.
            setMessages(mkMsgs([3, 4, 5, 6, 7]))
          })
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false,
            onJumpToSeq,
          })
          hook.attachListRef(div.el)

          hook.jumpToSeq(5n)
          // The seq, plus the abort signal that lets a caller who gave up stop this fetch.
          expect(onJumpToSeq).toHaveBeenCalledWith(5n, expect.any(AbortSignal))

          // Let the onJumpToSeq promise resolve, then the landing runs.
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(992) // scrollTopForAnchor(5) = 1000, minus 8
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('cancels the out-of-window landing when the user scrolls while the fetch is in flight', async () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(2500)
          const [messages, setMessages] = createSignal(mkMsgs([100, 101, 102, 103, 104]))
          let releaseFetch: (() => void) | undefined
          const onJumpToSeq = vi.fn(() => new Promise<void>((r) => {
            releaseFetch = r
          }))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false,
            onJumpToSeq,
          })
          hook.attachListRef(div.el)

          hook.jumpToSeq(5n)
          // The reader scrolls (a genuine wheel gesture, not an ambient layout scroll)
          // while the fetch is pending.
          hook.handlers.onWheel(new WheelEvent('wheel', { bubbles: true, deltaY: -100 }))
          div.setScrollTop(1800)
          hook.handlers.onScroll()

          // The fetch resolves and swaps the window -- but the landing must be cancelled.
          setMessages(mkMsgs([3, 4, 5, 6, 7]))
          releaseFetch?.()
          await Promise.resolve()
          await Promise.resolve()

          // The reader stays where they scrolled to; no seek-jump write clobbered it.
          expect(div.getScrollTop()).toBe(1800)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('does not cancel an out-of-window landing for ambient scroll noise before the fetch resolves', async () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(2500)
          const [messages, setMessages] = createSignal(mkMsgs([100, 101, 102, 103, 104]))
          let releaseFetch: (() => void) | undefined
          const onJumpToSeq = vi.fn(() => new Promise<void>((r) => {
            releaseFetch = r
          }))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false,
            onJumpToSeq,
          })
          hook.attachListRef(div.el)

          hook.jumpToSeq(5n)
          // Browser scroll anchoring/layout can emit a scroll event during the window swap
          // without a fresh input gesture. That should not clear the pending seek.
          div.setScrollTop(2450)
          hook.handlers.onScroll()

          setMessages(mkMsgs([3, 4, 5, 6, 7]))
          releaseFetch?.()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(992)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('abandons an in-flight out-of-window landing when cancelPendingSeek runs (a fresh thumb re-grab)', async () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(2500)
          const [messages, setMessages] = createSignal(mkMsgs([100, 101, 102, 103, 104]))
          let releaseFetch: (() => void) | undefined
          const onJumpToSeq = vi.fn(() => new Promise<void>((r) => {
            releaseFetch = r
          }))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false,
            onJumpToSeq,
          })
          hook.attachListRef(div.el)

          hook.jumpToSeq(5n)
          // A fresh thumb-drag grab takes manual control while the fetch is still pending.
          // The drag scrolls only programmatically, so it never trips the user-scroll seek
          // cancel -- the rail calls cancelPendingSeek explicitly on grab.
          hook.cancelPendingSeek()

          // The fetch resolves and swaps the window, but the abandoned seek must NOT land.
          setMessages(mkMsgs([3, 4, 5, 6, 7]))
          releaseFetch?.()
          await Promise.resolve()
          await Promise.resolve()

          // No seek-jump write clobbered the viewport; it is left for the live drag to drive.
          expect(div.getScrollTop()).toBe(2500)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  describe('abandoning an out-of-window seek aborts its fetch, not just its landing', () => {
    // Suppressing the landing leaves the page fetch running, and that fetch SWAPS THE WINDOW on
    // arrival. The reader then keeps the scroll position they took manual control of, while the
    // loaded window is centred on a target they abandoned -- so the next scroll-up or edge fill
    // pages around the wrong place. The signal is what actually stops it.

    /**
     * Stand up a hook whose out-of-window fetch stays pending until the test calls
     * `releaseFetch`, and capture its abort signal. An abort ends the seek, not the fetch.
     */
    function seekWithHeldFetch() {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(2500)
      const [messages] = createSignal(mkMsgs([100, 101, 102, 103, 104]))
      const signals: (AbortSignal | undefined)[] = []
      const releases: (() => void)[] = []
      const onJumpToSeq = vi.fn((_seq: bigint, signal?: AbortSignal) => {
        signals.push(signal)
        return new Promise<void>((resolve) => {
          releases.push(resolve)
        })
      })
      const hook = useChatScroll({
        virtualizer: seekVirt(),
        messages,
        hasOlderMessages: () => true,
        hasNewerMessages: () => false,
        onJumpToSeq,
      })
      hook.attachListRef(div.el)
      /** Complete every held fetch, as a fetch that the abort reaches too late does. */
      const releaseFetch = () => {
        for (const release of releases.splice(0))
          release()
      }
      return { hook, signals, div, releaseFetch }
    }

    it('aborts the fetch when cancelPendingSeek abandons the seek', () =>
      createRoot((dispose) => {
        const { hook, signals } = seekWithHeldFetch()
        void hook.jumpToSeq(5n)
        expect(signals[0]?.aborted).toBe(false)

        hook.cancelPendingSeek()
        expect(signals[0]?.aborted).toBe(true)
        dispose()
      }))

    it('aborts the previous fetch when a newer out-of-window jump supersedes it', () =>
      createRoot((dispose) => {
        const { hook, signals } = seekWithHeldFetch()
        void hook.jumpToSeq(5n)
        void hook.jumpToSeq(9n)
        // Two disjoint pages would otherwise race, and the loser could land last.
        expect(signals[0]?.aborted).toBe(true)
        expect(signals[1]?.aborted).toBe(false)
        dispose()
      }))

    /** A touch event whose `touches` list holds the fingers at `points`. */
    const touchesAt = (...points: [x: number, y: number][]) => ({
      touches: points.map(([clientX, clientY]) => ({ clientX, clientY })),
    }) as unknown as TouchEvent

    it('cancels a pending landing on a vertical touch move', async () => {
      let dispose!: () => void
      const { hook, signals, div, releaseFetch } = createRoot((d) => {
        dispose = d
        return seekWithHeldFetch()
      })
      const landing = hook.jumpToSeq(5n)
      expect(signals[0]?.aborted).toBe(false)
      const before = div.getScrollTop()

      hook.handlers.onTouchStart(touchesAt([10, 10]))
      hook.handlers.onTouchMove(touchesAt([10, 40]))

      expect(signals[0]?.aborted).toBe(true)
      // The fetch completes after the touch move. The landing that follows must not write:
      // without the cancel, it would land on the row nearest seq 5 at 992.
      const writes = div.scrollTopWrites()
      releaseFetch()
      expect(await landing).toBe(false)
      expect(div.getScrollTop()).toBe(before)
      expect(div.scrollTopWrites()).toBe(writes)
      dispose()
    })

    it('keeps a pending landing through a horizontal swipe', () =>
      createRoot((dispose) => {
        const { hook, signals } = seekWithHeldFetch()
        void hook.jumpToSeq(5n)
        // A sideways swipe on a wide code block: it scrolls nothing in the list.
        hook.handlers.onTouchStart(touchesAt([10, 10]))
        hook.handlers.onTouchMove(touchesAt([90, 30]))
        expect(signals[0]?.aborted).toBe(false)
        dispose()
      }))

    it('keeps a pending landing for a vertical move inside the touch slop', () =>
      createRoot((dispose) => {
        const { hook, signals } = seekWithHeldFetch()
        void hook.jumpToSeq(5n)
        hook.handlers.onTouchStart(touchesAt([10, 10]))
        hook.handlers.onTouchMove(touchesAt([10, 10 + PRESS_SLOP_PX]))
        expect(signals[0]?.aborted).toBe(false)
        hook.handlers.onTouchMove(touchesAt([10, 11 + PRESS_SLOP_PX]))
        expect(signals[0]?.aborted).toBe(true)
        dispose()
      }))

    it('keeps a pending landing through a pinch', () =>
      createRoot((dispose) => {
        const { hook, signals } = seekWithHeldFetch()
        void hook.jumpToSeq(5n)
        hook.handlers.onTouchStart(touchesAt([10, 10]))
        hook.handlers.onTouchStart(touchesAt([10, 10], [10, 200]))
        hook.handlers.onTouchMove(touchesAt([10, 60], [10, 150]))
        expect(signals[0]?.aborted).toBe(false)
        dispose()
      }))

    it('aborts a pending fetch when an IN-WINDOW jump supersedes it', () =>
      createRoot((dispose) => {
        const { hook, signals } = seekWithHeldFetch()
        void hook.jumpToSeq(5n)
        // 102 is inside the loaded span, so this jump lands at once and issues no fetch of its
        // own -- but the running one must still stop, or its swap arrives after this landing.
        void hook.jumpToSeq(102n)
        expect(signals[0]?.aborted).toBe(true)
        dispose()
      }))
  })

  it('writes a clamped programmatic scroll for in-window drag through previewScrollTo', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(0)
      const [messages] = createSignal(mkMsgs([1, 2, 3]))
      const hook = useChatScroll({
        virtualizer: seekVirt(),
        messages,
      })
      hook.attachListRef(div.el)

      hook.previewScrollTo(1200)
      expect(div.getScrollTop()).toBe(1200)
      // Clamped to the max scroll (scrollHeight - clientHeight = 4500).
      hook.previewScrollTo(99999)
      expect(div.getScrollTop()).toBe(4500)
      dispose()
    }))

  it('places a drag preview at the scroll coordinate of its content coordinate', () => {
    const h = mountScrollHarness()
    h.scrollTo(2000)
    // Row 0 grows by half a pixel. The rebase of the placement leaves a shift below one
    // pixel in place, so the scroll coordinate of content 1200 is 1199.5.
    h.changeAbove(0.5)

    h.hook.previewScrollTo(1200)

    expect(h.virt.layoutShift()).toBe(-0.5)
    expect(h.div.getScrollTop()).toBe(1199.5)
    h.dispose()
  })

  it('places a drag preview at the content top at the top of the scroller, above the inset of the spacer', () =>
    createRoot((dispose) => {
      const insetPx = 28
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000 + insetPx)
      div.setClientHeight(500)
      div.setScrollTop(2000)
      const [messages] = createSignal(mkMsgs([1, 2, 3]))
      const hook = useChatScroll({
        virtualizer: seekVirt(insetPx),
        messages,
      })
      hook.attachListRef(div.el)

      // Inside the content, a preview converts its content coordinate exactly.
      hook.previewScrollTo(1200)
      expect(div.getScrollTop()).toBe(1200 + insetPx)
      // The top end of a scrub reaches the top of the scroller, as the bottom end reaches its
      // bottom through the clamp: the inset above the first row is part of the history's top.
      hook.previewScrollTo(0)
      expect(div.getScrollTop()).toBe(0)
      hook.previewScrollTo(99999)
      expect(div.getScrollTop()).toBe(5000 + insetPx - 500)
      dispose()
    }))

  describe('an out-of-window seek from the bottom', () => {
    // The loaded window holds seqs 101..150 on the rows of the harness, so seq 21 is out of
    // it. The page around seq 21 holds seqs 1..50 on the same rows: row 20 has seq 21, and
    // its top is at 2000.
    const loadedWindow = rowMessages(101)
    const pageAroundTarget = rowMessages(1)

    /** A harness over the loaded window whose seek fetch stays pending until `fetch` settles. */
    function seekFromTheBottom(hasNewer: boolean) {
      const fetch = deferred<void>()
      const onJumpToSeq = vi.fn(() => fetch.promise)
      const h = mountScrollHarness({
        hasNewer,
        messages: loadedWindow,
        hookOptions: { onJumpToSeq },
      })
      return { h, fetch, onJumpToSeq }
    }

    it('keeps atBottom while the page loads, and clears it at the landing', async () => {
      const { h, fetch, onJumpToSeq } = seekFromTheBottom(false)
      // The mount follows the tail.
      expect(h.div.getScrollTop()).toBe(4500)
      expect(h.hook.atBottom()).toBe(true)

      const landing = h.hook.jumpToSeq(21n)
      expect(onJumpToSeq).toHaveBeenCalledOnce()
      // The view did not move, so the scroll-to-bottom button stays hidden.
      expect(h.hook.atBottom()).toBe(true)

      h.setMessages(pageAroundTarget)
      fetch.resolve()
      expect(await landing).toBe(true)

      expect(h.div.getScrollTop()).toBe(1992)
      expect(h.hook.atBottom()).toBe(false)
      h.dispose()
    })

    it('lands, and does not follow, when the swap reaches the live tail', async () => {
      const { h, fetch } = seekFromTheBottom(true)
      await h.anchoredAt(4500)
      expect(h.hook.atBottom()).toBe(true)
      expect(h.hook.isFollowing()).toBe(false)

      const landing = h.hook.jumpToSeq(21n)
      // The page is the latest one: the swap clears the newer pages under a reader whom
      // atBottom still places at the bottom, so the hook enters follow mode.
      h.setMessages(pageAroundTarget)
      h.setHasNewer(false)
      expect(h.hook.isFollowing()).toBe(true)
      // The landing is a placement, which ends follow mode.
      fetch.resolve()
      expect(await landing).toBe(true)

      expect(h.div.getScrollTop()).toBe(1992)
      expect(h.hook.isFollowing()).toBe(false)
      expect(h.hook.atBottom()).toBe(false)
      h.dispose()
    })

    it('returns to follow mode at the live bottom when the fetch fails', async () => {
      const { h, fetch } = seekFromTheBottom(false)
      const landing = h.hook.jumpToSeq(21n)
      // The seek ends the request for the tail before its fetch.
      expect(h.hook.isFollowing()).toBe(false)

      fetch.reject(new Error('the page did not load'))

      expect(await landing).toBe(false)
      // The view did not move, and the position check finds the reader at the live bottom.
      expect(h.div.getScrollTop()).toBe(4500)
      expect(h.hook.isFollowing()).toBe(true)
      expect(h.hook.atBottom()).toBe(true)
      h.dispose()
    })
  })

  it('renders the rows at the landing before its scroll event', async () => {
    // Row 20 has seq 21.
    const h = mountScrollHarness({
      messages: rowMessages(1),
    })
    h.updateViewport.mockClear()

    expect(await h.hook.jumpToSeq(21n)).toBe(true)

    expect(h.div.getScrollTop()).toBe(1992)
    expect(h.updateViewport).toHaveBeenLastCalledWith(1992, 500, undefined)
    h.dispose()
  })

  it('reports the placement of a seek to the virtualizer, with the position that it wrote', async () => {
    // Row 20 has seq 21.
    const h = mountScrollHarness({
      messages: rowMessages(1),
    })
    h.scrollTo(2400)
    // A scroll of the reader is no placement.
    expect(h.notePlacement).not.toHaveBeenCalled()

    expect(await h.hook.jumpToSeq(21n)).toBe(true)

    // The landing keeps 8px above the top of row 20, which is 2000.
    expect(h.div.getScrollTop()).toBe(1992)
    expect(h.notePlacement).toHaveBeenCalledTimes(1)
    expect(h.notePlacement).toHaveBeenLastCalledWith(1992)
    h.dispose()
  })

  it('resolves the landing row after the rebase', async () => {
    // Row 20 has seq 21.
    const messages = rowMessages(1)
    const h = mountScrollHarness({ messages })
    h.scrollTo(2000)
    h.scrollTo(1990)
    h.changeAbove(300)
    expect(h.rebaseCount()).toBe(0)

    expect(await h.hook.jumpToSeq(21n)).toBe(true)

    expect(h.rebaseCount()).toBe(1)
    expect(h.virt.layoutShift()).toBe(0)
    // The top of row 20 is 2300 after the change, and the landing keeps 8px above it.
    expect(h.div.getScrollTop()).toBe(2292)
    h.dispose()
  })
})
