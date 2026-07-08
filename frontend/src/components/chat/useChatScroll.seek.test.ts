import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, measurementDeferralNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

function mkMsgs(seqs: number[]): AgentChatMessage[] {
  return seqs.map(s => ({ id: `m${s}`, seq: BigInt(s), source: MessageSource.USER } as AgentChatMessage))
}

/** A virtualizer stub that resolves an anchor to `Number(seq) * 200` scrollTop. */
function seekVirt(): ChatScrollVirtualizer {
  return {
    ...measurementDeferralNoOps(),
    totalHeight: () => 5000,
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => ({ id: 'x', offsetWithinRow: 0 }),
    scrollTopNearAnchor: () => null,
    scrollTopForAnchor: (a: ScrollAnchor) => (a.seq !== undefined ? Number(a.seq) * 200 : null),
  }
}

describe('usechatscroll jump to seq', () => {
  it('lands on an in-window seq: writes the resolved row top minus the align offset', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(0)
      const [messages] = createSignal(mkMsgs([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]))
      const [streamingText] = createSignal('')
      const onJumpToSeq = vi.fn()
      const hook = useChatScroll({
        virtualizer: seekVirt(),
        messages,
        streamingText,
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
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            streamingText,
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
          reject(e instanceof Error ? e : new Error(String(e)))
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
          const [streamingText] = createSignal('')
          const onJumpToSeq = vi.fn(async (_seq: bigint) => {
            // The paginator swaps the window to a page around the target.
            setMessages(mkMsgs([3, 4, 5, 6, 7]))
          })
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false,
            onJumpToSeq,
          })
          hook.attachListRef(div.el)

          hook.jumpToSeq(5n)
          expect(onJumpToSeq).toHaveBeenCalledWith(5n)

          // Let the onJumpToSeq promise resolve, then the landing runs.
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(992) // scrollTopForAnchor(5) = 1000, minus 8
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
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
          const [streamingText] = createSignal('')
          let releaseFetch: (() => void) | undefined
          const onJumpToSeq = vi.fn(() => new Promise<void>((r) => {
            releaseFetch = r
          }))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            streamingText,
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
          reject(e instanceof Error ? e : new Error(String(e)))
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
          const [streamingText] = createSignal('')
          let releaseFetch: (() => void) | undefined
          const onJumpToSeq = vi.fn(() => new Promise<void>((r) => {
            releaseFetch = r
          }))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            streamingText,
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
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('cancelPendingSeek (a fresh thumb re-grab) abandons an in-flight out-of-window landing', async () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(2500)
          const [messages, setMessages] = createSignal(mkMsgs([100, 101, 102, 103, 104]))
          const [streamingText] = createSignal('')
          let releaseFetch: (() => void) | undefined
          const onJumpToSeq = vi.fn(() => new Promise<void>((r) => {
            releaseFetch = r
          }))
          const hook = useChatScroll({
            virtualizer: seekVirt(),
            messages,
            streamingText,
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
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('previewScrollTo writes a clamped programmatic scroll for in-window drag', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(0)
      const [messages] = createSignal(mkMsgs([1, 2, 3]))
      const [streamingText] = createSignal('')
      const hook = useChatScroll({ virtualizer: seekVirt(), messages, streamingText })
      hook.attachListRef(div.el)

      hook.previewScrollTo(1200)
      expect(div.getScrollTop()).toBe(1200)
      // Clamped to the max scroll (scrollHeight - clientHeight = 4500).
      hook.previewScrollTo(99999)
      expect(div.getScrollTop()).toBe(4500)
      dispose()
    }))
})
