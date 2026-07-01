import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'

import { maxScrollTopOf } from './chatScrollGeometry'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeGrowableVirtualizer, makeStubVirtualizer, measurementDeferralNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

describe('usechatscroll programmatic-write pagination guard', () => {
  it('does not swallow a user scroll that lands mid programmatic-write burst', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(4500)
      const [messages] = createSignal<AgentChatMessage[]>([])
      const [streamingText] = createSignal('')
      let olderLoads = 0
      const hook = useChatScroll({
        virtualizer: makeStubVirtualizer(),
        messages,
        streamingText,
        hasOlderMessages: () => true,
        onLoadOlderMessages: () => { olderLoads++ },
      })
      hook.attachListRef(div.el)

      // Programmatic write to the bottom records a pending programmatic position.
      hook.jumpToBottom() // -> scrollTop = scrollHeight (clamped to 4500)

      // The user scrolls to the very top BEFORE the programmatic marker clears
      // (no await -> the frame-delayed clear hasn't run). A position-matched
      // guard recognizes this as a real gesture (0 != 4500) and lets pagination
      // fire; the old frame-delayed boolean would have swallowed it.
      div.setScrollTop(0)
      hook.handlers.onScroll()
      expect(olderLoads).toBeGreaterThanOrEqual(1)

      dispose()
    }))

  it('consumes the echo so a later user scroll to the same pixel is not swallowed', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(2000)
      const [messages] = createSignal<AgentChatMessage[]>([])
      const [streamingText] = createSignal('')
      let newerLoads = 0
      const hook = useChatScroll({
        virtualizer: makeStubVirtualizer(),
        messages,
        streamingText,
        hasNewerMessages: () => true,
        onLoadNewerMessages: () => { newerLoads++ },
      })
      hook.attachListRef(div.el)

      // Programmatic jump to the bottom records scrollTop 4500 as our own write.
      hook.jumpToBottom() // scrollTop -> 4500 (clamped)

      // The echo scroll event at 4500 is recognized as ours: pagination is
      // suppressed, and the marker is CONSUMED (one write -> one echo).
      hook.handlers.onScroll()
      expect(newerLoads).toBe(0)

      // A SECOND scroll event at the exact same pixel (a coincidental user gesture
      // pressed against the bottom edge, no frame elapsed) is NOT a second echo:
      // the consumed marker no longer matches, so pagination fires. The old
      // position-only guard, whose marker persists until the next frame, would
      // have swallowed this gesture too.
      hook.handlers.onScroll()
      expect(newerLoads).toBe(1)

      dispose()
    }))
})

describe('usechatscroll auto-load through hidden-only window pages', () => {
  // makeStubVirtualizer reports totalHeight() === 0, which models a window with
  // no VISIBLE rows (e.g. a 150-message page that is entirely hidden items).
  it('auto-loads older when the window has no visible rows and older history exists', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          expect(olderLoads).toBeGreaterThanOrEqual(1)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('continues in the scrolled direction (down) first instead of firing both ways at once', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(0) // content fits the viewport -> non-scrollable
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const loadOrder: string[] = []
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => true,
            onLoadOlderMessages: () => loadOrder.push('older'),
            onLoadNewerMessages: () => loadOrder.push('newer'),
          })
          hook.attachListRef(div.el)
          // Downward intent: PageDown must page toward the tail FIRST (one side per
          // fill, never both at once). In an all-hidden window the filler later probes
          // the other side to find visible content, but the scrolled direction leads.
          hook.handlers.onKeyDown(new KeyboardEvent('keydown', { key: 'PageDown', cancelable: true }))
          await Promise.resolve()
          await Promise.resolve()
          expect(loadOrder.length).toBeGreaterThanOrEqual(1)
          expect(loadOrder[0]).toBe('newer')
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not auto-load when there is nothing more to load (genuinely empty)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let loads = 0
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasOlderMessages: () => false,
            hasNewerMessages: () => false,
            onLoadOlderMessages: () => { loads++ },
            onLoadNewerMessages: () => { loads++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          expect(loads).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('honors the preferred direction first in a non-scrollable window, never racing both in one fill', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(300) // visible content that fits -> non-scrollable
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(300) // totalHeight > 0 keeps the hidden-page auto-load inert
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const loadOrder: string[] = []
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => true,
            onLoadOlderMessages: () => loadOrder.push('older'),
            onLoadNewerMessages: () => loadOrder.push('newer'),
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          // A scroll event in a non-scrollable window pages ONE side per fill (the
          // if/else never fires both at once), honoring the default direction (older)
          // first. With both edges all-hidden the filler later probes the other side
          // to discover whether it is productive, but older always leads.
          hook.handlers.onScroll()
          expect(loadOrder.length).toBeGreaterThanOrEqual(1)
          expect(loadOrder[0]).toBe('older')
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('continues auto-advance through hidden-only pages until older history is exhausted', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(0)
          // The window keeps growing with more raw-but-hidden messages, so the
          // stub's totalHeight stays 0 (no visible rows) no matter how many pages load.
          // Each load is a real async fetch: it flips fetchingOlder true (so the
          // advance counts), then completes on a microtask, growing the window and
          // clearing the flag so the next advance can fire.
          let remaining = 25
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasOlderMessages: () => remaining > 0,
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                remaining--
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          // Drain the microtask-paced advance chain until hasOlderMessages turns false.
          for (let i = 0; i < 100; i++)
            await Promise.resolve()

          expect(olderLoads).toBe(25)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('stops when the older history runs out during a hidden-only run', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(0) // all-hidden: buffer above 0 < 1500, deficient
          // Only 3 older pages exist, all hidden (no visible height). Reaching the
          // start of history stops the fill.
          let remaining = 3
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasOlderMessages: () => remaining > 0,
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                remaining--
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          for (let i = 0; i < 50; i++)
            await Promise.resolve()

          // Loaded exactly the 3 available pages, then stopped (hasOlder false).
          expect(olderLoads).toBe(3)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('pre-fetches older pages AHEAD until the visible buffer above is filled, then stops (scrolled up)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500) // bufferTarget = 3 screens = 1500px; refill watermark = 2.5 screens = 1250px
          // A reader scrolled UP into history: 200px of visible buffer ABOVE the viewport
          // and 200px BELOW (distFromBottom), so we are OFF the live tail (isAtBottom
          // false) -- the older PRE-FETCH runs here (it is suppressed only while pinned at
          // the bottom; see suppressOlderPrefetchAtLiveTail).
          let scrollHeight = 900
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(200) // visible buffer ABOVE = 200, well under 1500; BELOW = 200
          const [total, setTotal] = createSignal(900)
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => total(),
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                // A VISIBLE older page lands: total + scrollHeight grow, and the re-pin
                // keeps the reader's row put, so the buffer ABOVE (scrollTop) grows too.
                const grow = 400
                scrollHeight += grow
                div.setScrollHeight(scrollHeight)
                div.setScrollTop(div.getScrollTop() + grow)
                setTotal(t => t + grow)
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          // Drain the microtask-paced fill until the buffer above reaches the refill
          // watermark. Hysteresis intentionally stops short of the ideal 3-screen target
          // so a tiny post-trim deficit does not fetch a full page and re-pin again.
          for (let i = 0; i < 50 && div.getScrollTop() < 1250; i++)
            await Promise.resolve()

          // Pre-fetched ahead to the refill watermark (a handful of pages, NOT a
          // thrash) then stopped. Every page made progress.
          expect(div.getScrollTop()).toBeGreaterThanOrEqual(1250)
          expect(div.getScrollTop()).toBeLessThan(1500)
          expect(olderLoads).toBeGreaterThanOrEqual(3)
          expect(olderLoads).toBeLessThanOrEqual(5)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does NOT speculatively pre-fetch older history while pinned at the live tail', () =>
    // Regression: a fresh mount at the bottom (page reload / HMR remount) must not page
    // older history to build the speculative above-buffer. The old behavior pre-fetched
    // it, and the prepend stream fought tail-follow during the re-measure storm, dragging
    // the view up-list and paging EVERY page. At the tail the older buffer is suppressed.
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500) // refill watermark = 2.5 screens = 1250px
          div.setScrollHeight(1000) // scrollable (maxScrollTop = 500)
          div.setScrollTop(500) // pinned at the bottom -- distFromBottom = 0
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 1000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true, // history exists AND the above-buffer (500px) is
            hasNewerMessages: () => false, // deficient (< 1250) -- the OLD code would page it
            fetchingOlder,
            // A broken suppression would page every page: each async load appends a
            // (still all-hidden) row and re-runs the fill, which stays deficient forever.
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          // Drain long enough that the old aggressive pre-fetch would have paged history.
          for (let i = 0; i < 40; i++)
            await Promise.resolve()

          // Nothing pre-fetched, and the view stayed pinned at the bottom (no prepend
          // storm to drag it up-list).
          expect(olderLoads).toBe(0)
          expect(div.getScrollTop()).toBe(500)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still fills a half-empty (non-scrollable) newest page at the tail until it is scrollable, then stops', () =>
    // The at-tail suppression must NOT reintroduce the half-empty-on-refresh bug: a
    // hidden-heavy newest page whose few visible rows do not fill the pane is
    // non-scrollable, so the older fill still runs -- but only until the pane becomes
    // scrollable, then it stops (it does not page every page).
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          let scrollHeight = 0 // half-empty: no visible content fills the pane
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(0)
          const [total, setTotal] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => total(),
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true, // unbounded history: any stop is the scrollable
            hasNewerMessages: () => false, // gate, not exhaustion
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                // A visible older page lands and the view resticks to the bottom.
                scrollHeight += 300
                div.setScrollHeight(scrollHeight)
                div.setScrollTop(scrollHeight - 500) // clamped to the bottom -- still at the tail
                setTotal(t => t + 300)
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          for (let i = 0; i < 40; i++)
            await Promise.resolve()

          // Filled the half-empty page (>= 1) but bounded (not every page); the pane is now
          // scrollable, at which point the tail suppression takes over.
          expect(olderLoads).toBeGreaterThanOrEqual(1)
          expect(olderLoads).toBeLessThanOrEqual(5)
          expect(maxScrollTopOf(div.el)).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still fills a BARELY-scrollable newest page (maxScrollTop inside the sticky band)', () =>
    // The 1-31px wedge: with 0 < maxScrollTop < the 32px sticky band, isAtBottom() is
    // true at EVERY scroll position, so the "first scroll-up leaves the tail and resumes
    // the fill" escape is mathematically unreachable -- a suppression gated on
    // maxScrollTop > 0 wedged older history off for good (a scrollbar-drag user fires
    // no wheel/key edge-intent loads either). Such a pane must keep filling, exactly
    // like the non-scrollable case, until it can genuinely leave the band.
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          let scrollHeight = 520 // maxScrollTop 20: scrollable, but only inside the band
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(20)
          const [total, setTotal] = createSignal(520)
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => total(),
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false,
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                scrollHeight += 300
                div.setScrollHeight(scrollHeight)
                div.setScrollTop(scrollHeight - 500) // restick keeps the view at the tail
                setTotal(t => t + 300)
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          for (let i = 0; i < 40; i++)
            await Promise.resolve()

          // At least one older page loaded (the wedge would have loaded ZERO), and the
          // fill stopped once the pane could leave the sticky band (bounded, not runaway).
          expect(olderLoads).toBeGreaterThanOrEqual(1)
          expect(olderLoads).toBeLessThanOrEqual(5)
          expect(maxScrollTopOf(div.el)).toBeGreaterThan(32)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still pre-fetches older at the loaded bottom of a window paged AWAY from the live tail', () =>
    // The at-tail suppression is gated on being at the TRUE live tail (hasNewer false).
    // A reader windowed away from the tail (hasNewer true) sits at the bottom of a
    // MID-history window, not the live bottom -- the older buffer is genuine there, so the
    // suppression must NOT bite. Guards the !hasNewerMessages condition against removal.
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(1000) // scrollable (maxScrollTop = 500)
          div.setScrollTop(500) // at the loaded bottom -- distFromBottom = 0
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 1000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => true, // windowed away from the live tail
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
            onLoadNewerMessages: () => {},
          })
          hook.attachListRef(div.el)
          for (let i = 0; i < 10; i++)
            await Promise.resolve()

          // The tail suppression does not apply off the live tail: older still pre-fetches.
          expect(olderLoads).toBeGreaterThanOrEqual(1)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not pre-fetch the buffer while the tab is hidden (clientHeight 0)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(0) // hidden/background tab: no laid-out viewport
          div.setScrollHeight(700)
          div.setScrollTop(200) // a deficient above-buffer were the tab visible
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 700,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
          })
          hook.attachListRef(div.el)
          // Drain long enough that a VISIBLE tab with this deficit would have
          // pre-fetched several pages by now.
          for (let i = 0; i < 30; i++)
            await Promise.resolve()

          // clientHeight 0 short-circuits the filler: nothing is pre-fetched for a
          // hidden tab.
          expect(olderLoads).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('pre-fetches NEWER pages to fill the buffer below (with the buffer above already full)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          let scrollHeight = 2200
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(1600) // buffer ABOVE = 1600 (>= 1500, satisfied); BELOW = 100
          const [total, setTotal] = createSignal(2200)
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => total(),
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingNewer, setFetchingNewer] = createSignal(false)
          const [streamingText] = createSignal('')
          let newerLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            fetchingNewer,
            onLoadNewerMessages: () => {
              newerLoads++
              setFetchingNewer(true)
              queueMicrotask(() => {
                // A newer page appends BELOW (grows the height) without moving the
                // viewport, so the buffer below (distFromBottom) grows.
                const grow = 400
                scrollHeight += grow
                div.setScrollHeight(scrollHeight)
                setTotal(t => t + grow)
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingNewer(false)
              })
            },
          })
          hook.attachListRef(div.el)
          const distBelow = () => div.el.scrollHeight - div.getScrollTop() - div.el.clientHeight
          for (let i = 0; i < 50 && distBelow() < 1250; i++)
            await Promise.resolve()

          // Filled the below-buffer to the refill watermark, bounded, no thrash. The
          // above-buffer was already full, so no older load fired.
          expect(distBelow()).toBeGreaterThanOrEqual(1250)
          expect(distBelow()).toBeLessThan(1500)
          expect(newerLoads).toBeGreaterThanOrEqual(3)
          expect(newerLoads).toBeLessThanOrEqual(5)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps loading an older all-hidden run while older history remains', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(900) // SOME visible content (mostly-hidden, not all-hidden)
          div.setScrollTop(200) // buffer above 200 < 1500 -> deficient
          // totalHeight never grows: every older page is hidden (no visible height).
          const [total] = createSignal(900)
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => total(),
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let remaining = 30
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => remaining > 0,
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                // Page lands but adds NO visible height (all hidden): geometry flat.
                remaining--
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          for (let i = 0; i < 100; i++)
            await Promise.resolve()

          expect(olderLoads).toBe(30)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('pauses the pre-fetch after a forced stop, then resumes on the next scroll', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          // Scrolled UP off the live tail (200px buffer above AND below), so the older
          // pre-fetch is eligible -- isolating the pause/resume behavior from the at-tail
          // suppression (see suppressOlderPrefetchAtLiveTail).
          div.setScrollHeight(900)
          div.setScrollTop(200) // buffer above 200 < 1500 -> deficient, would pre-fetch
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 900,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [fetchingOlder, setFetchingOlder] = createSignal(false)
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            fetchingOlder,
            onLoadOlderMessages: () => {
              olderLoads++
              setFetchingOlder(true)
              queueMicrotask(() => {
                setMessages(prev => [...prev, {} as AgentChatMessage])
                setFetchingOlder(false)
              })
            },
          })
          hook.attachListRef(div.el)
          // A forced stop BEFORE the first pre-fetch runs: pauses the buffer fill.
          hook.handlers.onPointerDown({ pointerType: 'mouse', clientY: 0 } as PointerEvent)
          for (let i = 0; i < 30; i++)
            await Promise.resolve()
          // Buffer is deficient, but paused by the stop -> nothing pre-fetched.
          expect(olderLoads).toBe(0)

          // A genuine scroll resumes the pre-fetch.
          div.setScrollTop(200)
          hook.handlers.onScroll()
          for (let i = 0; i < 30; i++)
            await Promise.resolve()
          expect(olderLoads).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('forceScrollToBottom resumes a buffer-fill paused by a forced stop (stop -> jump wedge)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          // A half-empty (non-scrollable) newest page at the tail: the older fill stays
          // eligible there (the exception to the at-tail suppression -- it fills the pane
          // until scrollable), so it is the observable for the stop -> jump resume.
          div.setScrollHeight(300)
          div.setScrollTop(0) // non-scrollable (maxScrollTop 0); older buffer deficient
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 300,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [streamingText] = createSignal('')
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => false, // forceScrollToBottom just sticks (no jump)
            onLoadOlderMessages: () => { olderLoads++ },
          })
          hook.attachListRef(div.el)
          // A forced stop pauses the pre-fetch; a window change does NOT resume it
          // (only a genuine scroll or a deliberate jump does).
          hook.handlers.onPointerDown({ pointerType: 'mouse', clientY: 0 } as PointerEvent)
          setMessages(prev => [...prev, {} as AgentChatMessage])
          for (let i = 0; i < 30; i++)
            await Promise.resolve()
          expect(olderLoads).toBe(0)

          // A deliberate jump-to-bottom (button / send) re-takes control of position and
          // must resume the pre-fetch -- it emits only programmatic echoes, so without
          // the explicit clear the fill stayed wedged off.
          hook.forceScrollToBottom()
          setMessages(prev => [...prev, {} as AgentChatMessage])
          for (let i = 0; i < 30; i++)
            await Promise.resolve()
          expect(olderLoads).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))
})

describe('usechatscroll scroll pagination dispatch', () => {
  it('loads a single direction (not both) when a small bidirectional window reads as near at both edges', () => {
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      // Content barely taller than the viewport: at scrollTop 0 the position is
      // near BOTH the top (0 < ch/2) and the bottom (distFromBottom 100 < ch/2).
      div.setClientHeight(500)
      div.setScrollHeight(600)
      div.setScrollTop(0)
      const [messages] = createSignal<AgentChatMessage[]>([])
      const [streamingText] = createSignal('')
      let olderLoads = 0
      let newerLoads = 0
      const hook = useChatScroll({
        virtualizer: makeStubVirtualizer(),
        messages,
        streamingText,
        hasOlderMessages: () => true,
        hasNewerMessages: () => true,
        onLoadOlderMessages: () => { olderLoads++ },
        onLoadNewerMessages: () => { newerLoads++ },
      })
      hook.attachListRef(div.el)
      // lastScrollDir defaults to 'older'. Firing both would let the newer fetch
      // abort the older one (beginHistoryFetch supersession), so only the older
      // load fires; the newer is suppressed.
      hook.handlers.onScroll()
      expect(olderLoads).toBe(1)
      expect(newerLoads).toBe(0)
      dispose()
    })
  })

  it('falls back to the opposite edge when the intended direction cannot load', () => {
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setClientHeight(500)
      div.setScrollHeight(5000)
      div.setScrollTop(4500) // pinned to the bottom, far from the top
      const [messages] = createSignal<AgentChatMessage[]>([])
      const [streamingText] = createSignal('')
      let olderLoads = 0
      let newerLoads = 0
      const hook = useChatScroll({
        virtualizer: makeStubVirtualizer(),
        messages,
        streamingText,
        hasOlderMessages: () => true,
        hasNewerMessages: () => true,
        onLoadOlderMessages: () => { olderLoads++ },
        onLoadNewerMessages: () => { newerLoads++ },
      })
      hook.attachListRef(div.el)
      // Stale lastScrollDir 'older', but the viewport is at the bottom (not near
      // the top): loadOlder no-ops, so the fallback loads newer.
      hook.handlers.onScroll()
      expect(olderLoads).toBe(0)
      expect(newerLoads).toBe(1)
      dispose()
    })
  })
})
