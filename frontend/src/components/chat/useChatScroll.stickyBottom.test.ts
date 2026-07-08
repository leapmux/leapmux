import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import {
  triggerResizeObserversSync,
} from '~/test-support/resizeObserverStub'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeGrowableVirtualizer, makeStubVirtualizer, measurementDeferralNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

describe('usechatscroll auto-scroll signature', () => {
  it('scrolls to bottom when agentStatus transitions from ACTIVE to STARTING', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // Viewport: content is 1000px tall, viewport 500px, user is at the
          // bottom (scrollTop = scrollHeight - clientHeight).
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [agentWorking] = createSignal<boolean | undefined>(false)
          const [agentStatus, setAgentStatus] = createSignal<AgentStatus | undefined>(AgentStatus.ACTIVE)

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            agentWorking,
            agentStatus,
          })
          hook.attachListRef(div.el)

          // Drain the initial createEffect run. The first run records the
          // current scrollHeight as lastAutoScrollHeight, so subsequent runs
          // only scroll when scrollHeight grows.
          await Promise.resolve()
          await Promise.resolve()

          // Simulate the inline AgentStartupBanner appearing: scrollHeight
          // grows because the banner is rendered after the message list.
          // The auto-scroll effect must re-run because agentStatus changed,
          // even though messages.length / messageVersion / streamingText did
          // not.
          div.setScrollHeight(1100)
          setAgentStatus(AgentStatus.STARTING)
          await Promise.resolve()
          await Promise.resolve()

          // Auto-scroll writes scrollTop = scrollHeight (1100); the fake div
          // clamps to scrollHeight - clientHeight (600), matching the real
          // browser behavior the hook relies on.
          expect(div.getScrollTop()).toBe(600)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('sticks to the bottom on a tail change even when scrollHeight is unchanged (trim+grow to identical height)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500) // at the bottom

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText, setStreamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          expect(hook.atBottom()).toBe(true)

          // A trim+grow nets the SAME scrollHeight (1000) but leaves the viewport one
          // message short of the bottom: scrollTop stays 400 while atBottom is still a
          // stale true (no scroll event fired). A height-equality gate would see
          // scrollHeight unchanged and skip the stick, stranding the view at 400.
          div.setScrollTop(400)
          setStreamingText('x') // a streaming chunk: the auto-scroll signature changes
          await Promise.resolve()
          await Promise.resolve()

          // The position-based gate sees we're NOT at the clamped bottom and sticks.
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

  it('does NOT auto-scroll when no tracked input changes (regression guard)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [agentWorking] = createSignal<boolean | undefined>(false)
          const [agentStatus] = createSignal<AgentStatus | undefined>(AgentStatus.ACTIVE)

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            agentWorking,
            agentStatus,
          })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()
          const baseline = div.getScrollTop()

          // Mutate only DOM scrollHeight without touching any tracked signal —
          // the effect must not re-run, so scrollTop stays put.
          div.setScrollHeight(1100)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(baseline)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still auto-sticks when the window collapses to a leading optimistic local (seq 0n)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const seqMsg = (s: bigint) => ({ seq: s } as AgentChatMessage)
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500) // at the bottom

          // Start with a server message so autoScrollFirstSeq records seq 5.
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([seqMsg(5n)])
          const [streamingText] = createSignal('')
          const [agentWorking, setAgentWorking] = createSignal<boolean | undefined>(false)

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            agentWorking,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // The server range empties to a single pending optimistic local (seq 0n)
          // at index 0, and content grows. seq 0n is smaller than the recorded
          // server seq 5, so a naive `msgs[0].seq < autoScrollFirstSeq` would read
          // this as an older-history PREPEND and suppress the bottom auto-stick.
          // Detecting the prepend off the first SERVER seq instead keeps the stick.
          div.setScrollHeight(1100)
          setMessages([seqMsg(0n)])
          setAgentWorking(true)
          await Promise.resolve()
          await Promise.resolve()

          // Auto-stuck: scrollTop = scrollHeight (1100) clamped to 1100 - 500.
          expect(div.getScrollTop()).toBe(600)
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

describe('usechatscroll resize sticky-bottom', () => {
  it('sticks to bottom when the viewport shrinks while at bottom', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // Content is 1000px tall, viewport 500px, user is at the bottom.
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          // Drain the initial onMount + ResizeObserver.observe wiring.
          await Promise.resolve()
          await Promise.resolve()

          // Editor auto-grow shrinks the messageList's parent, so
          // clientHeight decreases while content height stays the same.
          // Hook writes scrollTop = scrollHeight (1000); fake div clamps
          // to scrollHeight - clientHeight = 600, the new visual bottom.
          div.setClientHeight(400)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(600)
          expect(hook.atBottom()).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('preserves scrollTop when the viewport changes while not at bottom', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // User has scrolled up — well above the 32px sticky threshold.
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(200)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          // Make the hook observe an initial scroll position so atBottom is
          // false on entry to the resize.
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()

          // Editor auto-grows; viewport shrinks. scrollTop should NOT be
          // touched — preserving scrollTop naturally anchors the top of the
          // visible window for the user reading older content.
          div.setClientHeight(400)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(200)
          expect(hook.atBottom()).toBe(false)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-seats the scroll-direction baseline after a resize clamps scrollTop (no wrong-way pagination)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(2000) // scrolled up
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let olderLoads = 0
          let newerLoads = 0
          // Non-zero totalHeight so the hidden-page auto-advancer (which fires when
          // totalHeight()===0) stays out of this test; we want only the explicit
          // scroll events to dispatch pagination.
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            onLoadNewerMessages: () => { newerLoads++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // A downward scroll establishes a 'newer' direction and a baseline of 2000.
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          await Promise.resolve()

          // The pane grows tall enough that scrollTop clamps down to the new max
          // (100). Both edges are now "near" (content barely taller than viewport).
          div.setClientHeight(4900)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // A scroll event at the clamped position dispatches edge pagination in
          // the last direction. The baseline was re-seated to 100, so the prior
          // 'newer' intent is preserved -> loads NEWER. A stale 2000 baseline would
          // infer 'older' from 2000 -> 100 and wrongly load OLDER.
          hook.handlers.onScroll()
          await Promise.resolve()
          expect(newerLoads).toBe(1)
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

  it('pages newer at a fractional-DPI bottom edge where scrollBy leaves a sub-pixel residual', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // A fractional-DPI / browser-zoom content height: the clamped max is 4500.4.
          div.setScrollHeight(5000.4)
          div.setClientHeight(500)
          div.setScrollTop(4500) // 0.4px below the clamped max -- visually at the bottom
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let newerLoads = 0
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000.4,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => false,
            hasNewerMessages: () => true,
            onLoadNewerMessages: () => { newerLoads++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Ignore any mount-time pre-fetch; isolate the PageDown's effect.
          newerLoads = 0
          // PageDown at the fractional bottom: scrollBy clamps to 4500.4, leaving a
          // 0.4px residual vs `before` (4500). An exact `=== before` test would miss
          // it and never top up; the EDGE_INTENT_TOLERANCE_PX slack reads it as
          // no-move and fills newer so PageDown still loads beyond the loaded window.
          hook.pageScroll(1)
          await Promise.resolve()
          expect(div.getScrollTop()).toBeCloseTo(4500.4)
          expect(newerLoads).toBe(1)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('recomputes the rendered slice on a visible resize while scrolled up', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(200) // scrolled up -> the plain (not-at-bottom) branch

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let updateViewportCalls = 0
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 0,
            geometryVersion: () => 0,
            updateViewport: () => { updateViewportCalls += 1 },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }

          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          hook.handlers.onScroll() // atBottom() = false on entry
          await Promise.resolve()
          await Promise.resolve()

          const before = updateViewportCalls
          // A height-only resize (e.g. a vertical-divider drag) doesn't bump the
          // geometry effect, so the slice recompute -- needed to mount rows the
          // taller pane reveals and to apply the viewport-relative overscan -- must
          // come from the resize handler itself.
          div.setClientHeight(700)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          expect(updateViewportCalls).toBeGreaterThan(before)
          expect(hook.atBottom()).toBe(false) // still anchored, not yanked to the bottom
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('sticks to bottom on width-only change (sidebar toggle reflows text)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // User is at the bottom of a wide chat view.
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setClientWidth(800)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          // Showing a sidebar narrows the chat: width shrinks, text reflows
          // wider-wrap so scrollHeight grows. clientHeight is unchanged.
          // With overflow-anchor:none the browser doesn't move scrollTop, so
          // without sticky-bottom the user drifts off the bottom and atBottom
          // flips to false.
          div.setClientWidth(600)
          div.setScrollHeight(1100)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          // Hook writes scrollTop = scrollHeight (1100); the fake div clamps
          // to scrollHeight - clientHeight = 600, the new visual bottom.
          expect(div.getScrollTop()).toBe(600)
          expect(hook.atBottom()).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('preserves sticky bottom across a delayed programmatic scroll event after content grows', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          // Browser scroll events from the prior programmatic bottom write can
          // arrive after content has already grown. That delayed event should
          // be treated as stale and re-stick instead of clearing atBottom.
          div.setScrollHeight(1100)
          hook.handlers.onScroll()

          expect(div.getScrollTop()).toBe(600)
          expect(hook.atBottom()).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not snap scrollTop back to bottom when the user scrolls up a few pixels', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          // User flicks the touchpad up by a handful of pixels — well
          // within the 32px sticky threshold. The hook must NOT write
          // scrollTop back to the bottom; the user moved on purpose.
          div.setScrollTop(495)
          hook.handlers.onScroll()

          expect(div.getScrollTop()).toBe(495)

          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(495)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-sticks to bottom when the virtualized content grows (totalHeight)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setClientWidth(800)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()

          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          // A row measures taller (e.g. async syntax highlighting): the spacer
          // height grows. This now flows through the totalHeight effect, not a
          // content-element ResizeObserver, and must keep us pinned to bottom.
          div.setScrollHeight(1100)
          setTotal(1100)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(600)
          expect(hook.atBottom()).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('preserves sticky bottom through fast growth, delayed scroll, and content resize', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()

          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          hook.forceScrollToBottom()
          div.setScrollHeight(1100)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(600)
          expect(hook.atBottom()).toBe(true)

          // Content grows again (totalHeight) — re-stick to the new bottom.
          div.setScrollHeight(1250)
          setTotal(1250)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(750)
          expect(hook.atBottom()).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps bottom stickiness across a content shrink then grow (stale sticky record)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500) // at the bottom

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(1000)

          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Establish the sticky record at the current bottom (stickyScrollTop=500).
          hook.forceScrollToBottom()
          expect(div.getScrollTop()).toBe(500)
          expect(hook.atBottom()).toBe(true)

          // An over-estimated bottom row measures SMALLER (the estimator biases up):
          // the spacer shrinks and the browser clamps scrollTop down to the new,
          // lower bottom (300). The record must re-baseline to 300 here -- otherwise
          // it keeps the stale stickyScrollTop=500.
          div.setScrollHeight(800)
          setTotal(800)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // clamped to the new bottom
          expect(hook.atBottom()).toBe(true)

          // Content now GROWS (a later message / async highlight). With a stale
          // record, shouldRestickToBottom's `scrollTop(300)+1 >= stickyScrollTop(500)`
          // is false and the re-stick is dropped, stranding the view at 300. The
          // re-baseline keeps the record honest, so we follow to the new bottom.
          div.setScrollHeight(1200)
          setTotal(1200)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700) // followed to the new bottom (1200 - 500)
          expect(hook.atBottom()).toBe(true)
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

describe('usechatscroll getscrollstate', () => {
  it('saves a raw scrollTop fallback when scrolled away but no anchor resolves', () => {
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(5000)
      div.setClientHeight(500)
      div.setScrollTop(1000) // mid-list -> not at the bottom
      const [messages] = createSignal<AgentChatMessage[]>([])
      const [streamingText] = createSignal('')
      const hook = useChatScroll({
        // makeStubVirtualizer's anchorAt always returns null (empty/all-hidden
        // virtual list, totalHeight 0 -> no estimated spacer to drift against).
        virtualizer: makeStubVirtualizer(),
        messages,
        streamingText,
      })
      hook.attachListRef(div.el)
      hook.handlers.onScroll() // update atBottom() for the mid-list position
      expect(hook.isAtBottomFresh()).toBe(false)
      // No row anchor resolves, but the raw scrollTop is saved as a fallback so
      // the position isn't lost to bottom-follow on return (no spacer = no drift).
      const state = hook.getScrollState()
      expect(state?.anchor).toBeUndefined()
      expect(state?.rawScrollTop).toBe(1000)
      expect(state?.atBottom).toBe(false)
      dispose()
    })
  })

  it('still saves the at-bottom state even without an anchor', () => {
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(500)
      div.setClientHeight(500) // content fits -> at the bottom
      const [messages] = createSignal<AgentChatMessage[]>([])
      const [streamingText] = createSignal('')
      const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
      hook.attachListRef(div.el)
      hook.handlers.onScroll()
      const state = hook.getScrollState()
      expect(state).toBeDefined()
      expect(state!.atBottom).toBe(true)
      expect(state!.anchor).toBeUndefined()
      dispose()
    })
  })
})

describe('usechatscroll scroll-to-bottom animation', () => {
  it('hands off to sticky-bottom instead of chasing a target that grows every frame', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          let scrollHeight = 10000
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          hook.scrollToBottomAnimated()
          // Grow the target far faster than the step can close it each frame, so the
          // animation can only terminate via the frame cap -- never by reaching the
          // bottom. stickToBottom jumps scrollTop to the absolute bottom; the chase
          // (scrollTop += step) never lands there while the target keeps moving.
          let frames = 0
          let stuck = false
          while (frames < 200) {
            scrollHeight += 100000
            div.setScrollHeight(scrollHeight)
            await Promise.resolve() // run one animate frame against this height
            frames++
            if (div.getScrollTop() === scrollHeight - 500) {
              stuck = true
              break
            }
          }
          expect(stuck).toBe(true)
          // The frame cap is 60; the hand-off fires shortly after. An uncapped loop
          // would chase all the way to the 200-frame safety bound.
          expect(frames).toBeLessThanOrEqual(65)
          expect(hook.isAtBottomFresh()).toBe(true)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('a tap (pointerdown) mid-animation stops the coasting scroll immediately', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(10000)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park at the top AFTER the mount restick settles, so the animation genuinely
          // has 9500px to travel.
          div.setScrollTop(0)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(0)

          // Start the animation and grab the surface BEFORE its first frame delivers
          // (the testkit rAF fires on a microtask, so nothing has run yet). The grab's
          // cancelPendingScroll must cancel that queued frame so the view never moves.
          // (The testkit's rAF is cancelable for exactly this: with a no-op
          // cancelAnimationFrame the "cancelled" frame still fired, kept writing
          // scrollTop, and this regression was unobservable.)
          hook.scrollToBottomAnimated()
          hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(0) // the queued frame never ran
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
