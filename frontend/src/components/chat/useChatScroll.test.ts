import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { ChatScrollState, ChatScrollVirtualizer, ScrollContext } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createRenderEffect, createRoot, createSignal } from 'solid-js'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import {
  installControllableResizeObserver,
  triggerResizeObserversSync,
} from '../../../tests/unit/helpers/resizeObserverStub'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { FLING_SETTLE_MS } from './chatScrollFlingSettle'
import { inferScrollDirection } from './chatScrollGeometry'
import { createScrollInput } from './chatScrollInput'
import { createScrollVelocity } from './chatScrollVelocity'
import { computeBufferAwareKeepNewest, computeKeepNewest, FLING_OVERSCAN_MAX_PX, useChatScroll } from './useChatScroll'

/**
 * Minimal virtualizer stub. `totalHeight` is a constant so the geometry re-pin
 * effect never fires — these tests exercise the sticky-bottom / resize / scroll
 * logic, which is independent of the offset map. Tests that need anchoring
 * behavior drive the math in useChatVirtualizer.test.ts instead.
 */
function makeStubVirtualizer(): ChatScrollVirtualizer {
  return {
    totalHeight: () => 0,
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => null,
    scrollTopNearAnchor: () => null,
    scrollTopForAnchor: () => null,
  }
}

/**
 * A fully-stubbed ScrollContext (the shared scroll primitives the helpers reach back
 * into useChatScroll for). Pass overrides for the fields a test wires to its element
 * or asserts on; every other primitive defaults to a no-op.
 */
function makeScrollContext(overrides: Partial<ScrollContext> = {}): ScrollContext {
  return {
    getEl: () => undefined,
    virt: makeStubVirtualizer(),
    atBottom: () => false,
    setAtBottom: () => {},
    isAtBottom: () => false,
    isFollowing: () => false,
    isAnimating: () => false,
    followTail: () => {},
    refreshViewport: () => {},
    writeScrollTop: () => {},
    markProgrammaticScroll: () => {},
    syncVelocityToProgrammatic: () => {},
    setAnchor: () => {},
    ...overrides,
  }
}

/**
 * Virtualizer stub whose total height is a controllable signal. Bumping it
 * drives the geometry effect (the path that replaced the content-element
 * ResizeObserver for detecting content growth).
 */
function makeGrowableVirtualizer() {
  const [total, setTotal] = createSignal(0)
  const virt: ChatScrollVirtualizer = {
    totalHeight: () => total(),
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => null,
    scrollTopNearAnchor: () => null,
    scrollTopForAnchor: () => null,
  }
  return { virt, setTotal }
}

/**
 * Virtualizer stub backed by a mutable per-row height map, so a measurement that
 * grows or shrinks a row drives the geometry re-pin through a REAL offset map --
 * `anchorAt` / `scrollTopForAnchor` resolve against the current heights, exactly
 * like the production virtualizer. Row ids are `g${gen}_${index}`; `seq` mirrors
 * the index. `setRowHeight` mutates one row and bumps `geometryVersion`,
 * reproducing a DOM measurement landing after the row mounted (the
 * collapsed-until-measured rows the DOM-measurement pipeline grows once they
 * become visible). `replaceWindow` swaps in a fresh row set under a NEW generation
 * -- so every prior anchor id stops resolving -- modeling jumpToOldest/jumpToLatest
 * replacing the loaded window (`chatHistoryPaginator`), which is what leaves the
 * pre-jump anchor stale for the re-pin to recover from.
 */
function makeRowVirtualizer(initialHeights: number[]) {
  const [heights, setHeights] = createSignal<number[]>(initialHeights)
  const [gen, setGen] = createSignal(0)
  const [geometryVersion, setGeometryVersion] = createSignal(0)
  // Cumulative offsets, length n+1: cumOffsets[i] is the top of row i; cumOffsets[n] the
  // total. Zero-height rows share the offset of their successor, exactly reproducing the
  // collapsed-until-measured stack the real virtualizer builds.
  const cumOffsets = () => {
    const out = [0]
    for (const h of heights())
      out.push(out[out.length - 1] + h)
    return out
  }
  const total = () => {
    const o = cumOffsets()
    return o[o.length - 1]
  }
  // Row id encodes the window GENERATION so a replaced window's ids stop resolving (the
  // real virtualizer keys the offset map by row id; a re-fetched window has fresh rows).
  const indexOfId = (id: string): number => {
    const m = new RegExp(`^g${gen()}_(\\d+)$`).exec(id)
    if (!m)
      return -1
    const idx = Number(m[1])
    return idx >= 0 && idx < heights().length ? idx : -1
  }
  // Delegate the anchor math to the REAL pure functions over this geometry, so these
  // tests exercise the production capture/resolve (including the zero-height-run
  // tie-break) rather than a re-implementation that could drift from it.
  const geometry = (): AnchorOffsetGeometry => {
    const hs = heights()
    const offs = cumOffsets()
    return {
      list: hs.map((_, i) => ({ id: `g${gen()}_${i}`, seq: BigInt(i) })),
      // Largest index whose top offset <= y (the row containing y), clamped to [0, n-1].
      indexAtOffset: (y) => {
        let idx = 0
        for (let i = 0; i < hs.length; i++) {
          if (offs[i] <= y)
            idx = i
          else
            break
        }
        return idx
      },
      indexOfId,
      offsetOfIndex: i => offs[Math.max(0, Math.min(i, hs.length))],
      heightOfIndex: i => hs[i] ?? 0,
      gapAfter: () => 0,
    }
  }
  const virt: ChatScrollVirtualizer = {
    totalHeight: total,
    geometryVersion: () => geometryVersion(),
    updateViewport: () => {},
    anchorAt: (y: number): ScrollAnchor | null => anchorAtOffset(geometry(), y),
    scrollTopNearAnchor: (anchor: ScrollAnchor): number | null => resolveNearestAnchorScrollTop(geometry(), anchor),
    scrollTopForAnchor: (anchor: ScrollAnchor): number | null => resolveAnchorScrollTop(geometry(), anchor),
  }
  const setRowHeight = (idx: number, h: number) => {
    setHeights((prev) => {
      const next = [...prev]
      next[idx] = h
      return next
    })
    setGeometryVersion(v => v + 1)
  }
  const replaceWindow = (newHeights: number[]) => {
    setGen(g => g + 1)
    setHeights(newHeights)
    setGeometryVersion(v => v + 1)
  }
  return { virt, setRowHeight, replaceWindow, total }
}

beforeAll(() => {
  installControllableResizeObserver()
  // Run rAF synchronously on a microtask so tests can `await Promise.resolve()`
  // to flush scheduled scroll writes from the resize handler.
  globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
    queueMicrotask(() => cb(performance.now()))
    return 0
  }) as typeof requestAnimationFrame
  globalThis.cancelAnimationFrame = (() => {}) as typeof cancelAnimationFrame
})

// Backstop: many tests install fake timers and restore them only inside their own
// try/catch arms. If an assertion rejects in a microtask outside that wrapper, the
// faked clock would strand into the next test and mis-measure velocity. Restore
// timers (and any spies, defensively) unconditionally after every test so a single
// leak can't cascade. (The beforeAll rAF/ResizeObserver globals are direct
// assignments, not vi spies, so restoreAllMocks leaves them intact.)
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

interface FakeScrollDiv {
  el: HTMLDivElement
  setScrollHeight: (n: number) => void
  setClientHeight: (n: number) => void
  setClientWidth: (n: number) => void
  setScrollTop: (n: number) => void
  setRawScrollTop: (n: number) => void
  getScrollTop: () => number
}

/**
 * Build a real <div> with stubbed scroll/layout properties so the hook can
 * read scrollHeight / clientHeight / scrollTop and we can observe writes to
 * scrollTop. jsdom doesn't compute layout, so these have to be patched.
 *
 * `scrollTop` is clamped to [0, scrollHeight - clientHeight] on write to
 * match real browser behavior — the hook frequently uses
 * `scrollTop = scrollHeight` as a "scroll to bottom" idiom that relies on
 * that clamping to land at the actual visual bottom.
 */
function makeFakeScrollDiv(): FakeScrollDiv {
  const el = document.createElement('div')
  let scrollHeight = 0
  let clientHeight = 0
  let clientWidth = 0
  let scrollTop = 0
  const clamp = (v: number) => Math.max(0, Math.min(v, scrollHeight - clientHeight))
  Object.defineProperty(el, 'scrollHeight', {
    get: () => scrollHeight,
    configurable: true,
  })
  Object.defineProperty(el, 'clientHeight', {
    get: () => clientHeight,
    configurable: true,
  })
  Object.defineProperty(el, 'clientWidth', {
    get: () => clientWidth,
    configurable: true,
  })
  Object.defineProperty(el, 'scrollTop', {
    get: () => scrollTop,
    set: (v: number) => {
      scrollTop = clamp(v)
    },
    configurable: true,
  })
  // jsdom's scrollBy is a no-op; apply the vertical delta so pageScroll moves.
  el.scrollBy = ((opts?: ScrollToOptions | number) => {
    const top = typeof opts === 'number' ? 0 : (opts?.top ?? 0)
    scrollTop = clamp(scrollTop + top)
  }) as typeof el.scrollBy
  return {
    el,
    setScrollHeight: (n) => {
      scrollHeight = n
      scrollTop = clamp(scrollTop)
    },
    setClientHeight: (n) => {
      clientHeight = n
      scrollTop = clamp(scrollTop)
    },
    setClientWidth: (n) => {
      clientWidth = n
    },
    setScrollTop: (n) => {
      scrollTop = clamp(n)
    },
    // Safari/WebKit rubber-band overscroll can report negative scrollTop on read
    // even though normal assignments clamp. Tests use this to exercise the hook's
    // logical scroll-position normalization.
    setRawScrollTop: (n) => {
      scrollTop = n
    },
    getScrollTop: () => scrollTop,
  }
}

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

describe('createscrollvelocity', () => {
  // A controllable clock so velocity is deterministic (no real timing).
  function fakeClock(start = 0) {
    let t = start
    return {
      now: () => t,
      advance: (ms: number) => {
        t += ms
      },
    }
  }
  const make = (clock: { now: () => number }) =>
    createScrollVelocity({ now: clock.now, thresholdPxPerMs: 1, idleMs: 150 })

  it('reports a fling when velocity is unknown (no sample / one sample yet)', () => {
    const clock = fakeClock()
    const v = make(clock)
    // No sample: the first event of a gesture has no prior to measure against, so
    // it defers (preserves the prior always-defer behavior).
    expect(v.isFling()).toBe(true)
    v.sample(0)
    // One sample still can't bound a speed.
    expect(v.isFling()).toBe(true)
  })

  it('reports a fling for a fast cadence and not for a slow one', () => {
    const fast = fakeClock()
    const vf = make(fast)
    vf.sample(0)
    fast.advance(16)
    vf.sample(80) // 80px / 16ms = 5 px/ms >= 1 -> fling
    expect(vf.isFling()).toBe(true)

    const slow = fakeClock()
    const vs = make(slow)
    vs.sample(0)
    slow.advance(100)
    vs.sample(8) // 8px / 100ms = 0.08 px/ms < 1 -> deliberate scroll
    expect(vs.isFling()).toBe(false)
  })

  it('treats the threshold as inclusive (>= is a fling)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(10)
    v.sample(10) // exactly 1 px/ms
    expect(v.isFling()).toBe(true)
  })

  it('reports a fling again once the last sample goes stale (momentum has stopped)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(50)
    v.sample(4) // 0.08 px/ms -> slow
    expect(v.isFling()).toBe(false)
    // No new events for longer than idleMs: the slow reading is stale, so a fresh
    // gesture must not inherit it -- default back to deferring.
    clock.advance(200)
    expect(v.isFling()).toBe(true)
  })

  it('drops a same-tick (dt <= 0) sample without dividing by zero or absorbing its jump', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    v.sample(500) // same tick: dropped -> velocity stays unknown (no spurious Infinity flip)
    expect(v.isFling()).toBe(true)
  })

  it('measures the next interval from the last TIMED position across a coalesced sample', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(16)
    v.sample(100) // t=16: 100/16 = 6.25 px/ms, lastTime=16, lastPos=100
    // A coalesced re-emit at the SAME tick must NOT become the baseline -- otherwise
    // the next interval would measure 220-160 (absorbed jump) and under-report speed.
    v.sample(160) // same tick t=16: dropped
    clock.advance(16)
    v.sample(220) // t=32: |220 - 100| / 16 = 7.5 px/ms (from the last timed pos, span included)
    expect(v.speed()).toBe(7.5)
  })

  it('excludes a programmatic displacement from the measured gesture (no false fling)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0) // seed
    clock.advance(50)
    v.sample(4) // a slow deliberate scroll -> not a fling
    expect(v.isFling()).toBe(false)
    // A re-pin writes scrollTop +500 (a big prepend displacement in a hidden-heavy
    // window) -- NOT user gesture. The baseline moves with it so it doesn't count.
    v.syncToProgrammatic(504)
    clock.advance(100)
    // The user creeps another 50px: gesture = 50px/100ms = 0.5 px/ms, under the 1
    // px/ms threshold. WITHOUT the sync the delta would be (504+50)-4 = 550px over
    // 100ms = 5.5 px/ms and misclassify as a fling, deferring corrections that then
    // land as one overshoot.
    v.sample(554)
    expect(v.isFling()).toBe(false)
  })

  it('syncToProgrammatic is a no-op before the first sample (no baseline to move)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.syncToProgrammatic(1000) // nothing seeded yet
    expect(v.isFling()).toBe(true) // unknown velocity still defers
    clock.advance(16)
    v.sample(0)
    clock.advance(16)
    v.sample(200) // 200px / 16ms -> a genuine fast gesture is still a fling
    expect(v.isFling()).toBe(true)
  })

  // isActivelyFlinging differs from isFling: it is FALSE for the unknown seed and
  // while idle, so an async re-pin only abandons keep-position for a genuine live
  // fling whose momentum a write would cancel.
  it('isActivelyFlinging is false for the unknown (Infinity) seed, unlike isFling', () => {
    const clock = fakeClock()
    const v = make(clock)
    expect(v.isFling()).toBe(true)
    expect(v.isActivelyFlinging()).toBe(false) // nothing measured yet -> not a live fling
    v.sample(0)
    expect(v.isFling()).toBe(true)
    expect(v.isActivelyFlinging()).toBe(false) // one sample still can't bound a speed
  })

  it('isActivelyFlinging is true only for a measured, in-flight fast fling', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(16)
    v.sample(80) // 5 px/ms -> a measured fast fling
    expect(v.isActivelyFlinging()).toBe(true)
    // Momentum stops: no events for longer than idleMs -> no longer active.
    clock.advance(200)
    expect(v.isActivelyFlinging()).toBe(false)
    expect(v.isFling()).toBe(true) // isFling defaults back to defer; isActivelyFlinging does not
  })

  it('isActivelyFlinging is false for a slow deliberate scroll', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(100)
    v.sample(8) // 0.08 px/ms < 1 -> not a fling
    expect(v.isActivelyFlinging()).toBe(false)
  })

  // speed() drives the render-ahead overscan, so it must mirror isActivelyFlinging's
  // gating: 0 for the unknown seed and once idle, the measured px/ms otherwise.
  it('speed reports 0 until a velocity is measured, then the measured px/ms', () => {
    const clock = fakeClock()
    const v = make(clock)
    expect(v.speed()).toBe(0) // no sample -> no look-ahead
    v.sample(0)
    expect(v.speed()).toBe(0) // one sample: velocity is still the unknown Infinity seed
    clock.advance(16)
    v.sample(80) // 80px / 16ms = 5 px/ms
    expect(v.speed()).toBe(5)
  })

  it('speed decays to 0 once momentum goes stale (no render-ahead when idle)', () => {
    const clock = fakeClock()
    const v = make(clock)
    v.sample(0)
    clock.advance(16)
    v.sample(80) // 5 px/ms
    expect(v.speed()).toBe(5)
    clock.advance(200) // longer than idleMs (150): the reading is stale
    expect(v.speed()).toBe(0)
  })
})

describe('usechatscroll render-ahead overscan', () => {
  it('keeps the fling lead cap below the multi-screen mount burst budget', () => {
    // A 1200px cap left only ~2.3k px of forward coverage on a 733px pane once base
    // overscan was included, which can still expose blank spacer under coalesced
    // momentum. A 4000px cap mounted 30+ rows in one observed 732px-pane scroll commit.
    // Keep the cap in the middle: enough coverage for a delayed compositor frame, but
    // reject any return toward the old multi-screen mount burst.
    expect(FLING_OVERSCAN_MAX_PX).toBeGreaterThanOrEqual(1800)
    expect(FLING_OVERSCAN_MAX_PX).toBeLessThanOrEqual(2400)
  })

  it('extends the rendered slice ahead in the fling direction during a fast scroll', () =>
    new Promise<void>((resolve, reject) => {
      // Real timers (no fake): the velocity tracker reads a monotonic clock, so a
      // genuine time gap between the two scroll events is what measures a fast fling.
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(50000)
          div.setScrollTop(40000) // scrolled deep into a long transcript
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Record the look-ahead args handleScroll passes to updateViewport.
          let lastLeadPx = -1
          let lastLeadDir: 'older' | 'newer' | undefined = 'newer'
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 50000,
            geometryVersion: () => 0,
            updateViewport: (_st, _ch, lead) => {
              lastLeadPx = lead?.px ?? 0
              lastLeadDir = lead?.dir
            },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // First scroll establishes the velocity baseline; with no measured speed yet
          // (the unknown Infinity seed) there is no look-ahead. Read synchronously,
          // before any deferred refresh can overwrite it with the base (no-lead) slice.
          div.setScrollTop(39000)
          hook.handlers.onScroll()
          expect(lastLeadPx).toBe(0)

          // A real time gap, then a hard UP jump: a fast fling. The render-ahead must
          // be positive and point 'older' (the gesture direction) so the rows the next
          // frame lands on are already mounted.
          await new Promise(r => setTimeout(r, 20))
          div.setScrollTop(1000) // ~38000px up
          hook.handlers.onScroll()
          const flungLeadPx = lastLeadPx
          const flungLeadDir = lastLeadDir
          expect(flungLeadDir).toBe('older')
          // A ~38000px jump over ~20ms is far past the cap (velocity * look-ahead >>
          // FLING_OVERSCAN_MAX_PX), so the lead clamps to the ceiling deterministically.
          expect(flungLeadPx).toBe(FLING_OVERSCAN_MAX_PX)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('renders ahead toward the tail on a fast DOWNWARD fling (newer direction)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(50000)
          div.setScrollTop(1000) // near the top, away from the bottom
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let lastLeadPx = -1
          let lastLeadDir: 'older' | 'newer' | undefined = 'older'
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 50000,
            geometryVersion: () => 0,
            updateViewport: (_st, _ch, lead) => {
              lastLeadPx = lead?.px ?? 0
              lastLeadDir = lead?.dir
            },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed the baseline (no measured speed yet -> no look-ahead).
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(lastLeadPx).toBe(0)

          // A real gap, then a hard DOWN jump: the render-ahead points 'newer' and
          // clamps to the cap.
          await new Promise(r => setTimeout(r, 20))
          div.setScrollTop(40000) // ~38000px down
          hook.handlers.onScroll()
          const flungLeadPx = lastLeadPx
          const flungLeadDir = lastLeadDir
          expect(flungLeadDir).toBe('newer')
          expect(flungLeadPx).toBe(FLING_OVERSCAN_MAX_PX)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops the lead on a zero-delta event instead of pointing the stale direction', () =>
    new Promise<void>((resolve, reject) => {
      // Fake timers freeze the velocity tracker's monotonic clock (perf.now), so two
      // samples at the SAME scrollTop with NO time advance produce dt=0 -- the
      // coalesced same-tick case where speed() keeps the prior fling value. That is
      // exactly the state where the lead must NOT trust the stale lastScrollDir.
      vi.useFakeTimers()
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(50000)
          div.setScrollTop(40000)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let lastLeadPx = -1
          let lastLeadDir: 'older' | 'newer' | undefined
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 50000,
            geometryVersion: () => 0,
            updateViewport: (_st, _ch, lead) => {
              lastLeadPx = lead?.px ?? 0
              lastLeadDir = lead?.dir
            },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed, then a 20ms gap and a hard UP jump = a fast 'older' fling at the cap.
          div.setScrollTop(39000)
          hook.handlers.onScroll()
          vi.advanceTimersByTime(20)
          div.setScrollTop(1000)
          hook.handlers.onScroll()
          expect(lastLeadDir).toBe('older')
          expect(lastLeadPx).toBe(FLING_OVERSCAN_MAX_PX)

          // A coalesced same-tick event at the SAME scrollTop (no time advance): speed()
          // still reads the fling value (dt=0 kept it), but the direction is now
          // indeterminate. The lead must collapse to none rather than extend 'older'
          // (the side the viewport just LEFT) and flash an unrendered gap.
          hook.handlers.onScroll()
          expect(lastLeadPx).toBe(0)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))
})

describe('usechatscroll windowing trim', () => {
  const mkMsgs = (n: number): AgentChatMessage[] =>
    Array.from({ length: n }, (_, i) => ({ seq: BigInt(i + 1) } as AgentChatMessage))

  it('trims the oldest end while following the tail even after the geometry re-pin advanced the sticky record', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500) // at the bottom
          // A window already at the cap: one new tail message pushes it over.
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(1000)
          let trims = 0
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            onTrimOldMessages: (k) => {
              trims++
              lastKeep = k
            },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Establish the sticky record at the current bottom while following.
          hook.forceScrollToBottom()
          expect(hook.atBottom()).toBe(true)

          // A new tail message arrives (length crosses the cap) AND the spacer
          // grows. The geometry re-pin effect is created before the auto-scroll
          // effect, so it runs first: it re-sticks to the new bottom and advances
          // stickyScrollHeight to the grown value. The auto-scroll effect then
          // sees scrollHeight === stickyScrollHeight. The trim must STILL fire --
          // it's the only window cap while following the tail (pagination trims
          // don't run then), so gating it on the height delta would let the
          // in-memory set grow unbounded during streaming.
          div.setScrollHeight(1100)
          setTotal(1100)
          setMessages(mkMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(600) // followed to the new bottom
          expect(trims).toBeGreaterThanOrEqual(1)
          // keepNewest is buffer-aware: less than a buffer (1500px) of visible content
          // sits above the 600px bottom here, so it keeps the whole window rather than
          // reaping rows the filler surfaced (the trim/refetch loop). A NORMAL followed
          // tail has far more than a buffer of visible content above the bottom, so
          // keepNewest clamps to the lean base instead.
          expect(lastKeep).toBe(MAX_LOADED_CHAT_MESSAGES + 1)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('trims the oldest end while SCROLLED UP at the live tail (not just while following)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500) // start at the bottom
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(5000)
          let trims = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            // Still AT the live tail (no newer history unloaded), so live messages
            // append to the tail rather than being dropped by the beyond-window guard.
            hasNewerMessages: () => false,
            onTrimOldMessages: () => { trims++ },
          })
          // (This stub resolves no anchor, so currentAnchor stays null and keepNewest
          // is 0 -- the viewport-protecting keepNewest is covered by the next test.)
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Now scroll UP and register the position so atBottom is false.
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A live message arrives while the user is scrolled up watching history.
          // Before the fix the trim was gated behind atBottom, so the in-memory
          // window grew unbounded during a long stream. It must fire regardless of
          // scroll position -- it's the only window cap at the live tail.
          setMessages(mkMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(trims).toBeGreaterThanOrEqual(1)
          // The user's scroll position is NOT yanked to the bottom by the append.
          expect(hook.atBottom()).toBe(false)
          expect(div.getScrollTop()).toBe(2000)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not trim the older buffer after an older prepend wakes the auto-scroll effect again', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(8000)
          div.setClientHeight(733)
          div.setScrollTop(2500)
          const mkSeqMsgs = (first: number, last: number): AgentChatMessage[] =>
            Array.from({ length: last - first + 1 }, (_, i) => {
              const seq = BigInt(first + i)
              return { id: `m${seq}`, seq } as AgentChatMessage
            })
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkSeqMsgs(101, 250))
          const [streamingText, setStreamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(8000)
          let trims = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: () => { trims++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          trims = 0

          // Older prefetch: the first server seq moves earlier, while the newest edge
          // is unchanged. The auto-scroll effect must treat this as an older-buffer
          // mutation, not as live-tail growth to cap.
          setMessages(mkSeqMsgs(51, 250))
          await Promise.resolve()
          await Promise.resolve()
          expect(trims).toBe(0)

          // A later non-row wake-up used to see the stale pre-prepend signature and
          // trim the just-prefetched older rows, producing a prepend/trim re-pin pair.
          setStreamingText('tail is still streaming')
          await Promise.resolve()
          await Promise.resolve()
          expect(trims).toBe(0)

          // A real newest-edge row append still runs the live-tail cap.
          setMessages(mkSeqMsgs(51, 251))
          await Promise.resolve()
          await Promise.resolve()
          expect(trims).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still trims when an older prepend also advances the newest edge', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(8000)
          div.setClientHeight(733)
          div.setScrollTop(2500)
          const mkSeqMsgs = (first: number, last: number): AgentChatMessage[] =>
            Array.from({ length: last - first + 1 }, (_, i) => {
              const seq = BigInt(first + i)
              return { id: `m${seq}`, seq } as AgentChatMessage
            })
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkSeqMsgs(101, 250))
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(8000)
          let trims = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: () => { trims++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          trims = 0

          setMessages(mkSeqMsgs(51, 251))
          await Promise.resolve()
          await Promise.resolve()

          expect(trims).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('passes a viewport-protecting keepNewest so the oldest trim spares the reader\'s anchored row', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500) // start at the bottom
          // Rows carry ids so the trim can locate the anchored row in the window.
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          // A virtualizer that pins the viewport midpoint to row 'm50' (the reader
          // scrolled up to it). totalHeight constant so the geometry effect is quiet.
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm50', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up so the anchor (row m50) is captured.
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A live message appends at the tail (window now 151 rows). The trim must
          // keep from the anchored row m50 (index 49) to the tail: 151 - 49 = 102,
          // so the reader's viewport rows are never dropped.
          setMessages(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(102)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('excludes trailing optimistic locals from keepNewest so the trim cap stays on server rows', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)
          // Server rows (seq 1..n) followed by two trailing optimistic locals
          // (seq 0n) -- the exact shape the store keeps while a send is in flight.
          const mkMsgs = (server: number): AgentChatMessage[] => [
            ...Array.from({ length: server }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage)),
            { id: 'local-1', seq: 0n } as AgentChatMessage,
            { id: 'local-2', seq: 0n } as AgentChatMessage,
          ]
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm50', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // Append a live SERVER row: window is now 151 server + 2 locals (153).
          // The anchor m50 sits at index 49; the SERVER rows from there to the tail
          // are 151 - 49 = 102. The two trailing locals must NOT inflate the cap
          // (counting them would yield 104 and over-retain two old server rows).
          setMessages(mkMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(102)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('protects the whole window when the anchored row was displaced out of the list', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          // The captured anchor row is NOT present in the window (deleted / reseq'd
          // out between capture and the trim), so findIndex returns -1.
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'gone', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A displaced anchor must NOT drop keepNewest to 0 (the normal cap),
          // which could trim rows the reader can still see. Protect the whole
          // current window instead; the store clamps it to the hard ceiling.
          setMessages(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(MAX_LOADED_CHAT_MESSAGES + 1)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('recaptures a resolvable anchor when displaced, so the trim keeps the viewport cap not the whole window', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)
          const mkIdMsgs = (start: number, n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${start + i}`, seq: BigInt(start + i) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(1, MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          // anchorAt resolves to m1 while it's present (capture), then to a present
          // MIDDLE row m10 once m1 is gone -- modelling an optimistic local that
          // reconciled to a server echo under a new id between capture and trim.
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: messages().some(m => m.id === 'm1') ? 'm1' : 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(2000)
          hook.handlers.onScroll() // captures anchor = m1
          expect(hook.atBottom()).toBe(false)

          // Replace the window so m1 (the anchor) is gone but m10 is present in the
          // MIDDLE. Length changes so the auto-scroll signature wakes the trim.
          const replaced = mkIdMsgs(5, MAX_LOADED_CHAT_MESSAGES + 1) // m5..m(MAX+5)
          setMessages(replaced)
          await Promise.resolve()
          await Promise.resolve()

          // m1 displaced -> recapture lands on m10 (a middle row), so keepNewest is
          // the viewport cap (rows from m10 down), NOT the whole-window fallback and
          // NOT 0. Without the recapture this would protect the whole window.
          const m10Idx = replaced.findIndex(m => m.id === 'm10')
          expect(m10Idx).toBeGreaterThan(0)
          expect(lastKeep).toBe(replaced.length - m10Idx)
          expect(lastKeep).toBeLessThan(replaced.length)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keepNewest retains the older pre-fetch buffer ABOVE the viewport, beyond the base cap', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const rowH = 100
          const div = makeFakeScrollDiv()
          // clientHeight 2000 -> bufferTargetPx = 3 * 2000 = 6000px = 60 rows of buffer.
          div.setClientHeight(2000)
          div.setScrollHeight(40000)
          div.setScrollTop(28000) // scrolled up; viewport top at row index 280 (m281)
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(400))
          const [streamingText] = createSignal('')
          // A virtualizer whose anchorAt maps a scroll offset to the row at that offset
          // (rows are rowH tall), so the buffer-top anchor (bufferTargetPx above the
          // viewport top) resolves to a row STRICTLY above the viewport anchor.
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 40000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: (y: number) => ({ id: `m${Math.max(0, Math.floor(y / rowH)) + 1}`, offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: (a: { id: string }) => (Number(a.id.slice(1)) - 1) * rowH,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up: capture the viewport-midpoint anchor m291 (index 290).
          div.setScrollTop(28000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A live message appends at the tail (window now 401 rows). The trim keeps
          // from the row bufferTargetPx (6000px = 60 rows) ABOVE the viewport top --
          // index 220 (m221) -- down to the tail: 401 - 220 = 181. Crucially that
          // EXCEEDS the base cap (150), so the store (Math.max(150, keepNewest)) actually
          // retains the buffer. The OLD viewport-only keepNewest would be 401 - 280 = 121,
          // which clamps to just the base 150 -- reaping the 31-row older buffer the
          // filler maintains and forcing a refetch. The midpoint anchor alone would keep
          // even less (401 - 290 = 111), so this scenario is production-
          // observable: 181 kept vs 150.
          setMessages(mkIdMsgs(401))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(181)
          expect(lastKeep).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES) // beyond the base cap -> real retention
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('applies the lean base cap (keepNewest 0) while the tab is hidden (clientHeight 0)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(0) // hidden/inactive tab -- viewport & buffer unmeasurable
          div.setScrollHeight(5000)
          div.setScrollTop(2000)
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            // Would resolve a buffer-top row (from the stale scrollTop) if consulted --
            // the clientHeight===0 guard must skip the buffer-aware computation entirely.
            anchorAt: (y: number) => ({ id: `m${Math.max(0, Math.floor(y / 100)) + 1}`, offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // A tail append fires the trim while the tab is hidden. The viewport/buffer
          // can't be measured, so keepNewest is the lean base (0) -- NOT a buffer-aware
          // value derived from the stale scrollTop (which would be ~131 here).
          setMessages(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(0)
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

describe('usechatscroll scroll coordinate normalization', () => {
  it('uses clamped scrollTop for viewport and anchor logic during rubber-band overscroll', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const updateViewport = vi.fn()
          const anchorAt = vi.fn((top: number): ScrollAnchor => ({ id: `top@${top}`, offsetWithinRow: 0 }))
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport,
            anchorAt,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          updateViewport.mockClear()
          anchorAt.mockClear()
          div.setRawScrollTop(-12)

          hook.handlers.onScroll()

          // At the very top edge the top-ROW anchor is captured, so anchorAt reads the
          // clamped logical top (0), NOT the raw -12 overscroll value -- proving the
          // anchor logic operates in clamped scroll coordinates.
          expect(anchorAt).toHaveBeenCalledWith(0)
          expect(updateViewport).toHaveBeenCalledWith(0, 500, undefined)
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
})

describe('usechatscroll anchor re-pin', () => {
  // A controllable virtualizer where the anchored row sits at `rowOffset`.
  // `prepend(px)` simulates older history landing above it: the row's offset
  // and the total height both grow, which should re-pin scrollTop so the row
  // stays visually put.
  function makeControllableVirtualizer() {
    let total = 1000
    let rowOffset = 90
    const [version, setVersion] = createSignal(0)
    const virt: ChatScrollVirtualizer = {
      totalHeight: () => {
        version()
        return total
      },
      geometryVersion: version,
      updateViewport: () => {},
      anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - rowOffset }),
      scrollTopNearAnchor: () => null,
      scrollTopForAnchor: a => rowOffset + a.offsetWithinRow,
    }
    return {
      virt,
      prepend: (px: number) => {
        rowOffset += px
        total += px
        setVersion(v => v + 1)
      },
      // Shift the anchored row WITHOUT changing total height — e.g. a row above
      // grows while one below shrinks. Only geometryVersion changes, so this
      // exercises the geometryVersion dependency of the re-pin effect.
      shiftWithoutTotalChange: (px: number) => {
        rowOffset += px
        setVersion(v => v + 1)
      },
    }
  }

  it('keeps the anchored row stationary when older messages are prepended', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // User scrolls up; the viewport midpoint sits 260px into the row at offset 90.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // 600px of older content lands above the anchored row.
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()

          // The row moved from offset 90 to 690; scrollTop tracks it to keep the same
          // 260px-into-the-row midpoint position (690 + 260 - 250), NOT snap to bottom.
          expect(div.getScrollTop()).toBe(700)
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

  it('translates stale native momentum events after a large prepend re-pin', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Capture the viewport's row at scrollTop 100 (rowOffset 90,
          // offsetWithinRow 10), then prepend 600px of older history. The anchor
          // re-pin correctly translates the viewport to 700.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          hook.handlers.onWheel({ deltaX: 0, deltaY: -1, ctrlKey: false } as WheelEvent)
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700)

          // Chromium/WebKit can still deliver a native momentum scroll event in the
          // OLD coordinate space after the re-pin write. This event represents the
          // user's continued -20px movement from 100 -> 80; it must be translated
          // into the NEW coordinate space (700 -> 680), not accepted as scrollTop 80.
          div.setScrollTop(80)
          hook.handlers.onScroll()

          expect(div.getScrollTop()).toBe(680)
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

  it('does not translate current-coordinate momentum that merely crosses the old midpoint', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Same setup as the stale-native translation case: the user is scrolling
          // older, then a prepend re-pin shifts the current coordinate space from
          // 100 -> 700.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          hook.handlers.onWheel({ deltaX: 0, deltaY: -1, ctrlKey: false } as WheelEvent)
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700)

          // This is a normal native momentum event in the NEW coordinate space,
          // continuing upward from 700. It is now >300px from afterTop and near the
          // old coordinate, so the prior midpoint-only heuristic misclassified it
          // as stale and translated it to 980. Since it did NOT move from the old
          // beforeTop in the user's older direction, it must be left alone.
          div.setScrollTop(380)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(380)

          // The first current-coordinate event disarms the stale-shift window. Even
          // if a later current-coordinate scroll reaches the old side within the
          // original timeout, it must not be translated by the obsolete +600 shift.
          div.setScrollTop(80)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(80)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not translate current-coordinate momentum after a compensating re-pin cancels the shift', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(100)
          hook.handlers.onScroll()
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700)

          // A far-above trim can remove most of the just-added coordinate-space shift
          // while keeping the same anchored row visually fixed. Once this compensating
          // re-pin leaves only a small net movement, subsequent native momentum scroll
          // events are already in the CURRENT coordinate space and must not be
          // translated by the stale +600 prepend delta.
          ctrl.prepend(-520)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(180)

          div.setScrollTop(160)
          hook.handlers.onScroll()

          expect(div.getScrollTop()).toBe(160)
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

  it('translates stale native momentum that is near the old coordinate even if intent changed', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // The last known wheel intent was older, then a large older-page prepend
          // re-pins the viewport from old coordinate 100 to current coordinate 1600.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          hook.handlers.onWheel({ deltaX: 0, deltaY: -1, ctrlKey: false } as WheelEvent)
          ctrl.prepend(1500)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(1600)

          // Chromium/WebKit can still deliver a delayed native momentum event in
          // the old coordinate space. The sampled old-coordinate position can be
          // slightly *newer* than the pre-repin top even though the last recorded
          // intent was older (coalescing, bounce-back, or a direction reversal).
          // Because it is still close to the old coordinate and far from the new
          // coordinate, it must translate by the coordinate-space shift rather
          // than being accepted as current-coordinate scrollTop 280.
          div.setScrollTop(280)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(1780)
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

  it('tracks the latest captured anchor across consecutive geometry changes', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(200)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up to 200, then 100px of content above the anchor measures in.
          div.setScrollTop(200)
          hook.handlers.onScroll()
          ctrl.prepend(100) // rowOffset 90 -> 190
          await Promise.resolve()
          await Promise.resolve()
          // Anchor (offsetWithinRow 110) stays put: 190 + 110.
          expect(div.getScrollTop()).toBe(300)

          // User scrolls up again; the anchor must re-capture to the NEW position
          // (not the stale one), so the next correction tracks it.
          div.setScrollTop(120)
          hook.handlers.onScroll()
          ctrl.prepend(100) // rowOffset 190 -> 290
          await Promise.resolve()
          await Promise.resolve()
          // New anchor (offsetWithinRow 120 - 190 = -70): 290 + (-70).
          expect(div.getScrollTop()).toBe(220)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not snap to the bottom when geometry changes while atBottom is stale (fast scroll up)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // Start pinned to the bottom.
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          expect(hook.atBottom()).toBe(true)

          // The user flicks up fast: the browser has already moved scrollTop up,
          // but a measurement-driven geometry change lands and re-pins BEFORE the
          // scroll event is processed — so the atBottom signal is still stale-true.
          div.setScrollTop(100)
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()

          // Re-pin must NOT yank the view back to the bottom against the user's
          // scroll. (Sticking is owned by the auto-scroll effect + ResizeObserver,
          // both of which see a fresh scroll position.)
          expect(div.getScrollTop()).toBe(100)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-anchors instead of yanking when a fling outran the anchor capture', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4000)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Capture an anchor deep in the list (scrollTop 4000 -> the row at offset
          // 90, offsetWithinRow 3910). scrollTopForAnchor would resolve it back to
          // 4000.
          div.setScrollTop(4000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A fast fling carries scrollTop far up (to 100) WITHOUT a scroll event
          // re-capturing the anchor -- momentum outran the per-event captures. A row
          // mounting mid-fling now bumps geometryVersion and fires the async re-pin.
          div.setScrollTop(100)
          ctrl.shiftWithoutTotalChange(0) // geometryVersion ticks; rowOffset unchanged
          await Promise.resolve()
          await Promise.resolve()

          // The re-pin must NOT yank scrollTop back to the stale anchor at 4000
          // (a +3900 reversal of the fling). It re-anchors to the live viewport
          // instead, leaving the fling intact at 100.
          expect(div.getScrollTop()).toBe(100)

          // ...and the re-anchored row is now live: a later keep-position shift
          // tracks the row that was under the viewport at 100 (offset 90,
          // offsetWithinRow 10), moving with it to 150 -- NOT back toward 4000.
          ctrl.shiftWithoutTotalChange(50) // rowOffset 90 -> 140
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(150)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('compensates (writes) a large keep-position shift over a stationary viewport mid-fling', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(300)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Two scroll samples a few ms apart establish a MEASURED fast fling (the
          // viewport is moving up under momentum). dt > 0 keeps the velocity finite
          // (a live fling, not the unknown seed). The anchor is captured at 100.
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await new Promise(r => setTimeout(r, 10))
          div.setScrollTop(100)
          hook.handlers.onScroll()

          // A page-sized keep-position shift grows the anchor's offset by 300px while
          // the viewport (scrollTop 100) and the capture point stay put -- movedSinceCapture
          // is ~0, so this is NOT the flungAway (stale-anchor) case. It is a real prepend/
          // trim above the anchor. The correction (300) exceeds flingSuppressPx (250): the
          // OLD code DROPPED it as "too big to write mid-fling", leaking the whole 300px as
          // cumulative scroll drift. The fix WRITES it -- keep-position must compensate a
          // structural shift even mid-fling, or the shift's full height leaks.
          ctrl.shiftWithoutTotalChange(300) // rowOffset 90 -> 390; scrollTopForAnchor 400
          await Promise.resolve()
          await Promise.resolve()

          // scrollTop moved to keep the anchored row stationary (390 + offsetWithinRow 10),
          // NOT left at 100 (the old leak).
          expect(div.getScrollTop()).toBe(400)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-pins on a geometry change that leaves total height unchanged', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Anchor the viewport midpoint to the row at offset 90 (offsetWithinRow 260).
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A row above the anchor grows while one below shrinks: the anchored
          // row moves down by 50px but total height is unchanged. Only
          // geometryVersion ticks. The re-pin must still fire — keying solely on
          // totalHeight would miss this and let the row jump.
          ctrl.shiftWithoutTotalChange(50) // rowOffset 90 -> 140
          await Promise.resolve()
          await Promise.resolve()

          // scrollTop follows the row: 140 + 260 - midpoint offset 250.
          expect(div.getScrollTop()).toBe(150)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('restores the pre-jump anchor when forceScrollToBottom\'s jump-to-latest fails', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            // The jump-to-latest RPC fails (e.g. worker disconnect).
            onJumpToLatest: () => Promise.reject(new Error('worker offline')),
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up so the viewport midpoint anchors to the row at offset 90.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // Click scroll-to-bottom: it optimistically follows the tail, then the
          // jump rejects. No scroll write happens until the jump resolves, so the
          // view is still at scrollTop 100.
          hook.forceScrollToBottom()
          // Let the rejected jump promise settle through .catch.
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          // atBottom re-synced to the real (scrolled-up) position.
          expect(hook.atBottom()).toBe(false)

          // A later geometry change must still re-pin to the RESTORED anchor: the
          // row moves from 90 to 690 and scrollTop tracks it (690 + 260 - 250). Had the
          // failed jump left the anchor null, scrollTop would stay at 100.
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-anchors to the current position when a jump-to-latest fails while following the tail', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest: () => Promise.reject(new Error('worker offline')),
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // No scroll-up: the hook is FOLLOWING the tail, so currentAnchor() is
          // null. Clicking scroll-to-bottom (shown because hasNewerMessages) jumps,
          // and the jump rejects. With no pre-jump anchor to restore, the catch
          // must re-anchor to the CURRENT scroll position rather than re-entering
          // 'following' at a tail it never reached.
          hook.forceScrollToBottom()
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          // A later geometry change re-pins to the freshly-captured anchor: the row
          // at scrollTop 100 (offset 90) moves to 690 and scrollTop tracks it (700).
          // Had the failed jump left the mode 'following' (null anchor), the re-pin
          // would not move scrollTop and it would stay at 100.
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('scrollToBottom routes to the jump-to-latest force path while windowed away from the tail', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          let jumps = 0
          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest: () => {
              jumps++
              return Promise.resolve()
            },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // hasNewerMessages: the loaded bottom isn't the live tail, so the windowing-
          // aware scrollToBottom jumps to the latest page (force path) rather than an
          // in-window animated scroll.
          hook.scrollToBottom()
          await Promise.resolve()
          expect(jumps).toBe(1)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('scrollToBottom stays in-window (no jump-to-latest) when the tail is loaded', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()

          let jumps = 0
          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onJumpToLatest: () => {
              jumps++
              return Promise.resolve()
            },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // The tail is in the window: an animated in-window scroll, never a
          // jump-to-latest.
          hook.scrollToBottom()
          await Promise.resolve()
          expect(jumps).toBe(0)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not yank to the bottom if the user scrolled up while forceScrollToBottom\'s jump was in flight', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()
          let resolveJump: () => void = () => {}

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            // A SLOW jump-to-latest: stays in flight until we resolve it below.
            onJumpToLatest: () => new Promise<void>((r) => { resolveJump = r }),
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Click scroll-to-bottom: optimistically follows the tail, jump in flight.
          hook.forceScrollToBottom()
          expect(hook.atBottom()).toBe(true)

          // User scrolls UP while the jump is in flight: handleScroll captures an
          // anchor and clears atBottom.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // The jump resolves. Its .then() must HONOR the mid-flight scroll-up and
          // NOT stick to the bottom (which would yank scrollTop to scrollHeight).
          resolveJump()
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          expect(hook.atBottom()).toBe(false)
          expect(div.getScrollTop()).toBe(100) // not yanked to the bottom
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('preserves the user-captured anchor if forceScrollToBottom\'s jump fails after a mid-flight scroll', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()
          let rejectJump: (reason?: unknown) => void = () => {}

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest: () => new Promise<void>((_, reject) => { rejectJump = reject }),
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Establish a pre-jump anchor at scrollTop 100.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          hook.forceScrollToBottom()
          expect(hook.atBottom()).toBe(true)

          // The user scrolls to a different position while the jump is still pending.
          // handleScroll captures that newer midpoint anchor and clears atBottom.
          div.setScrollTop(300)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          rejectJump(new Error('worker offline'))
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          // A later prepend should preserve the USER'S mid-flight anchor at 300:
          // rowOffset 90 -> 690, offsetWithinRow 460, midpoint offset 250 => 900.
          // Restoring the stale pre-jump anchor would land at 700 instead.
          ctrl.prepend(600)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(900)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-syncs atBottom instead of sticking when a live append leaves newer messages during forceScrollToBottom\'s jump', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const ctrl = makeControllableVirtualizer()
          let resolveJump: () => void = () => {}

          const hook = useChatScroll({
            virtualizer: ctrl.virt,
            messages,
            streamingText,
            // The window is STILL short of the live tail after the jump resolves -- a
            // live append landed during the in-flight jump, so hasNewerMessages stays
            // true throughout.
            hasNewerMessages: () => true,
            onJumpToLatest: () => new Promise<void>((r) => { resolveJump = r }),
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Click scroll-to-bottom: optimistically follows the tail, jump in flight.
          hook.forceScrollToBottom()
          expect(hook.atBottom()).toBe(true)

          // No user scroll this time, but the jump resolves with newer messages still
          // beyond the window. The .then() must NOT stick to the loaded bottom (a
          // non-live-tail position); it re-syncs atBottom to the real scroll position
          // so the scroll-to-bottom affordance reappears instead of pinning a stale
          // bottom.
          resolveJump()
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          expect(hook.atBottom()).toBe(false)
          expect(div.getScrollTop()).toBe(100) // not yanked to a non-live-tail bottom
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('re-anchors to the current row when the anchored row is trimmed out of the window', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(300)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          // Controllable virt: only `resolvableId` resolves; anchorAt always
          // reports the row currently at the viewport midpoint.
          let resolvableId = 'row-a'
          let currentTopId = 'row-a'
          let rowOffset = 290
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return 5000
            },
            geometryVersion: version,
            updateViewport: () => {},
            anchorAt: scrollTop => ({ id: currentTopId, offsetWithinRow: scrollTop - rowOffset }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === resolvableId ? rowOffset + a.offsetWithinRow : null),
          }

          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up -> capture anchor { id: 'row-a', offsetWithinRow: 260 }.
          div.setScrollTop(300)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // 'row-a' is trimmed out of the window: it no longer resolves, and the
          // row now at the viewport midpoint is 'row-b'. The re-pin must re-anchor to
          // 'row-b' (scrollTop unchanged), not silently keep the dead anchor.
          resolvableId = 'row-b'
          currentTopId = 'row-b'
          setVersion(v => v + 1)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)

          // A later prepend grows offsets: the re-anchored 'row-b' tracks it
          // (890 + 260 - midpoint offset 250). Had the hook kept the dead 'row-a' anchor,
          // scrollTopForAnchor would stay null and scrollTop would never correct.
          rowOffset = 890
          setVersion(v => v + 1)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(900)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not jump when a newly-mounted row measures multiple viewport-heights tall', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          // Large scrollHeight so the corrected scrollTop never clamps.
          div.setScrollHeight(40000)
          div.setScrollTop(300)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          // ~6 viewport-heights tall (3000px) for the row that mounts during the
          // armed scroll. `updateViewport` (called by refreshViewport) models that
          // mount measuring its real height: the offset map shifts and a geometry
          // bump fires the re-pin. `armed` defers the shift until after we've
          // established a scrolled-up state, so the mount-time auto-scroll doesn't
          // consume it. Before the shift the anchored row sits at offset 290;
          // after it a tall row occupies the space above, so the SAME (un-
          // corrected) scrollTop now maps to a different row ('tall-row').
          const SHIFT = 3000
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: (a) => {
              if (a.id === 'anchored-row')
                return (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              // The wrong anchor (captured against the shifted map) resolves back
              // to the un-corrected scrollTop -> the visible jump.
              return a.offsetWithinRow
            },
          }

          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          // The mount-time auto-scroll stuck us to the bottom, leaving anchor=null.

          // First scroll-up: the tall row mounts during refreshViewport and
          // measures 3000px, which synchronously fires the geometry re-pin. With
          // no prior anchor, the re-pin must use the anchor captured from THIS
          // event against the PRE-shift geometry, so it keeps the row the user
          // scrolled to visually stationary: that row moved 3000px down in the
          // spacer, so scrollTop tracks to 300 + 3000. Capturing the anchor after
          // the shift leaves the re-pin with no usable anchor, so scrollTop stays
          // at 300 and the content jumps (the tall row fills the viewport).
          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          expect(hook.atBottom()).toBe(false)
          expect(div.getScrollTop()).toBe(300 + SHIFT)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('skips a sub-pixel re-pin write (no-op correction does not touch scrollTop)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [version, setVersion] = createSignal(0)
          // A measurement below the midpoint anchor: scrollTopForAnchor resolves to within
          // a sub-pixel of the current viewport midpoint (the anchor didn't move).
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return 5000
            },
            geometryVersion: version,
            updateViewport: () => {},
            anchorAt: top => ({ id: 'a', offsetWithinRow: top }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => a.offsetWithinRow + 0.4,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(300)
          hook.handlers.onScroll() // capture the anchor
          // An idle geometry change fires the re-pin; the 0.4px correction is a
          // no-op, so scrollTop is left untouched (a write would interrupt a fling).
          setVersion(v => v + 1)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('suppresses a small re-pin correction during an active user scroll (preserves momentum)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // A small (100px) same-flush geometry shift during the user's scroll.
          // 100 < clientHeight/2 (250), so the re-pin suppresses the correction:
          // scrollTop stays where the user flung (no write to cancel momentum).
          // Visible-row measurements use a separate deferral queue; this stub pins
          // the re-pin policy for geometry that has already committed.
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          // Correction would have been 300 -> 400; suppressed during the fling.
          expect(div.getScrollTop()).toBe(300)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('absorbs a medium re-pin correction during a slow native scroll', () =>
    new Promise<void>((resolve, reject) => {
      // Fake performance.now too, so the velocity tracker sees a controllable,
      // genuinely-slow cadence instead of two same-tick (Infinity) samples.
      vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Same 100px under-estimate shift the fling test defers -- but reached by a
          // SLOW native scroll. It is still a browser-owned scroll event, so writing
          // scrollTop here fights the user's trajectory; absorb/re-anchor instead.
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // First scroll event seeds the velocity baseline (no shift armed yet).
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()

          // 100ms later, a 10px nudge = 0.1 px/ms, far below the fling threshold.
          // The row mounts+shifts during THIS event, so the re-pin fires. A medium
          // estimate correction is absorbed rather than written (310 -> 410 would
          // be a visible bounce in the slow tail of a trackpad scroll).
          vi.advanceTimersByTime(100)
          armed = true
          div.setScrollTop(310)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(310)

          vi.useRealTimers()
          dispose()
          resolve()
        }
        catch (e) {
          vi.useRealTimers()
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops the deferred fling correction once momentum stops', () =>
    new Promise<void>((resolve, reject) => {
      // Fake only setTimeout/clearTimeout so the rAF microtask mock + queueMicrotask
      // still drive the synchronous scroll writes; advance to fire the settle.
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Same setup as the suppress test: a 100px shift (< clientHeight/2) is
          // deferred during the fling. Once momentum stops, the current visual
          // position wins; the settle re-anchors there instead of snapping by 100px.
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          // Deferred during the fling — scrollTop stays where momentum left it.
          expect(div.getScrollTop()).toBe(300)

          // Momentum stops (no further scroll events): the debounce fires and drops
          // the deferred correction instead of applying a post-fling snap.
          vi.advanceTimersByTime(200)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('applies a keyboard-page re-pin correction immediately instead of deferring it as fling drift', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // The same 100px under-estimate shift the fling tests use, but reached
          // via a discrete keyboard PageDown rather than momentum. A discrete page
          // must apply the correction immediately, not defer it as fling drift.
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300) // re-seat after mount effects settle scrollTop
          // PageDown advances scrollTop by clientHeight - overlap (452) -> 752 and
          // flags the resulting scroll event as a discrete page.
          hook.handlers.onKeyDown(new KeyboardEvent('keydown', { key: 'PageDown', cancelable: true }))
          expect(div.getScrollTop()).toBe(752)
          // Simulate the browser's native scroll event for that scrollBy.
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          // The 100px correction is applied RIGHT AWAY (752 -> 852), not suppressed.
          expect(div.getScrollTop()).toBe(852)
          // No fling-settle was armed, so advancing past the debounce changes
          // nothing -- a fling would only now accept and re-anchor deferred drift.
          vi.advanceTimersByTime(300)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(852)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('applies a re-pin correction immediately during a pointer DRAG instead of deferring it as fling drift', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // The same 100px under-estimate shift the fling tests defer, but reached
          // while a pointer is DOWN (a scrollbar drag). A drag has no momentum to
          // protect, so the correction must apply immediately, not defer.
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300) // re-seat after mount effects settle scrollTop
          // A scrollbar drag: the pointer is down while the scroll fires.
          hook.handlers.onPointerDown({ pointerType: 'mouse', clientY: 0, pointerId: 1, isPrimary: true } as PointerEvent)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          // The 100px correction applies RIGHT AWAY (300 -> 400), not suppressed.
          expect(div.getScrollTop()).toBe(400)
          // Nothing was deferred, so advancing past the settle debounce is a no-op.
          vi.advanceTimersByTime(300)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(400)
          // Releasing the pointer returns to fling semantics for later momentum.
          hook.handlers.onPointerUp({ pointerId: 1 } as PointerEvent)
          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('recovers drag tracking after a dropped pointerup, so a later release returns to fling semantics', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          // A pointer goes down (drag) but its pointerup is DROPPED (pointer-capture
          // transfer / gesture intercept) -- with a hand-balanced counter this would
          // latch isScrollInputActive() true forever. A fresh PRIMARY pointer begins a
          // new gesture and must clear the stale id, so its release leaves no pointer
          // tracked and momentum semantics resume.
          hook.handlers.onPointerDown({ pointerType: 'mouse', clientY: 0, pointerId: 1, isPrimary: true } as PointerEvent)
          // (no onPointerUp for pointerId 1 -- the up was dropped)
          hook.handlers.onPointerDown({ pointerType: 'mouse', clientY: 0, pointerId: 2, isPrimary: true } as PointerEvent)
          hook.handlers.onPointerUp({ pointerId: 2 } as PointerEvent)

          // No pointer is down now, so the shift-triggering scroll is treated as
          // MOMENTUM and the correction DEFERS (a stuck counter would apply it
          // immediately, like the drag test above).
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // deferred/absorbed, NOT corrected to 400
          vi.advanceTimersByTime(300)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // no post-momentum settle snap

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps drag tracking active while a second finger remains, releasing only on the last touchend', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow,
          }
          // touchActive is derived from the live touch list, so lifting ONE finger of
          // a two-finger gesture must keep it active (a finger remains) -- the drag
          // correction stays immediate until the last finger lifts.
          const touchEvent = (count: number) => ({ touches: Array.from({ length: count }, () => ({ clientY: 0 })) } as unknown as TouchEvent)
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onTouchStart(touchEvent(1)) // first finger
          hook.handlers.onTouchStart(touchEvent(2)) // second finger
          hook.handlers.onTouchEnd(touchEvent(1)) // one lifted, one REMAINS -> still active
          // A drag is in progress (a finger is down), so the correction applies now.
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(400) // immediate (drag), not deferred

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('matches a discrete page by position, so an interleaving fling cannot steal it', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300) // re-seat after mount effects settle scrollTop
          // PageDown arms the discrete page targeting scrollTop 752.
          hook.handlers.onKeyDown(new KeyboardEvent('keydown', { key: 'PageDown', cancelable: true }))
          expect(div.getScrollTop()).toBe(752)
          // A fling event interleaves at a DIFFERENT position before the page's own
          // native scroll event. With the old "next scroll event is the page" flag
          // this fling would be mistaken for the discrete page (immediate re-pin);
          // matched by position it is correctly treated as a fling -> the 100px
          // correction is DEFERRED, leaving scrollTop where momentum left it.
          div.setScrollTop(600)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(600)
          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not write a deferred correction while momentum events continue', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers 100px; arms the settle
          await Promise.resolve()
          await Promise.resolve()
          // A near-end momentum frame (95ms < 150ms) arrives before the settle.
          vi.advanceTimersByTime(95)
          hook.handlers.onScroll() // resets the debounce -> no settle yet
          await Promise.resolve()
          vi.advanceTimersByTime(95) // 190ms since the FIRST event, only 95 since the last
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // still deferred -> momentum not cancelled
          // Now genuinely idle past the threshold.
          vi.advanceTimersByTime(100)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // idle settle accepts the current position

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('defers visible measurement commits during momentum and flushes them after the quiet settle', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let deferred = false
          const setVisibleMeasurementDeferral = vi.fn((next: boolean) => {
            deferred = next
          })
          const flushDeferredMeasurements = vi.fn(() => {
            deferred = false
            return true
          })
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 8000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: anchor => anchor.offsetWithinRow,
            setVisibleMeasurementDeferral,
            hasDeferredMeasurements: () => deferred,
            flushDeferredMeasurements,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(300)
          hook.handlers.onWheel({ deltaX: 0, deltaY: -120, ctrlKey: false } as WheelEvent)
          hook.handlers.onScroll()
          await Promise.resolve()
          expect(setVisibleMeasurementDeferral).toHaveBeenCalledWith(true)
          expect(flushDeferredMeasurements).not.toHaveBeenCalled()

          vi.advanceTimersByTime(FLING_SETTLE_MS)
          await Promise.resolve()

          expect(setVisibleMeasurementDeferral).toHaveBeenLastCalledWith(false)
          expect(flushDeferredMeasurements).toHaveBeenCalledTimes(1)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('cancels the deferred fling-settle when the user grabs to stop (no coast-on write)', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers 100px; arms the settle
          await Promise.resolve()
          await Promise.resolve()

          // The user grabs the surface to stop the fling. That must cancel the pending
          // settle, so advancing past the debounce does NOT write the deferred 100px --
          // the view stops immediately where the user stopped it.
          hook.handlers.onPointerDown({ pointerType: 'mouse', clientY: 0 } as PointerEvent)
          vi.advanceTimersByTime(300)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // NOT 400 -- no coast-on settle

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('cancels the deferred fling-settle on a no-movement wheel event (deltaX and deltaY both 0)', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers 100px; arms the settle
          await Promise.resolve()
          await Promise.resolve()

          // The browser fires a no-movement wheel event (deltaX and deltaY both 0)
          // as the trackpad momentum-cancel when the user rests fingers on the surface
          // to stop an inertial scroll. It must cancel the pending settle, so advancing
          // past the debounce does NOT write the deferred 100px -- the view stops
          // immediately where the user stopped it, just like the pointer-grab path.
          hook.handlers.onWheel({ deltaX: 0, deltaY: 0, ctrlKey: false } as WheelEvent)
          vi.advanceTimersByTime(300)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // NOT 400 -- no coast-on settle

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops a deferred fling correction once it sticks to the bottom (no stale carry-over)', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // A NEGATIVE shift: content above the anchor shrinks, so keeping the
          // anchored row stationary would pull scrollTop DOWN by 100 — deferred
          // during the fling (|100| < clientHeight/2). flingDrift = -100.
          const SHIFT = -100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => shifted
              ? { id: 'tall-row', offsetWithinRow: scrollTop }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers -100; arms the settle
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // deferred, momentum preserved

          // Before the settle fires the user jumps to the bottom (anchor cleared,
          // scrollTop clamped to the visual bottom 39500).
          hook.jumpToBottom()
          expect(div.getScrollTop()).toBe(39500)

          // Settle fires. The deferred -100 no longer applies (we're following the
          // tail now): it must be dropped, NOT applied as 39500 - 100 = 39400,
          // which would yank the view off the bottom.
          vi.advanceTimersByTime(200)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(39500)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops a deferred fling correction when a trim removes the anchored row mid-fling', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          let trimmed = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => trimmed
              ? { id: 'replacement-row', offsetWithinRow: 0 }
              : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET },
            // Once trimmed, the anchored row no longer resolves (null) -> the re-pin
            // re-anchors to 'replacement-row' and drops the deferred drift.
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (trimmed ? null : (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow)
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers +100 against 'anchored-row'; arms settle
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // deferred, momentum preserved

          // The anchored row is trimmed out of the window before the settle fires.
          trimmed = true
          setVersion(v => v + 1)
          await Promise.resolve()
          await Promise.resolve()

          // Settle fires: the drift was dropped at the trim re-anchor, so scrollTop
          // is NOT yanked to 400 (which would apply this fling's overshoot to the
          // unrelated row that replaced the trimmed one).
          vi.advanceTimersByTime(200)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does NOT suppress a small re-pin on a programmatic-echo scroll (only user flings)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Two scroll events, each committing a geometry shift during
          // refreshViewport (synchronous re-pin). Phase 1 is a
          // user scroll with a LARGE (400px) shift: 400 >= clientHeight/2 (250) so
          // it writes (300 -> 700) and records 700 as the programmatic position.
          // Phase 2 is the browser's echo of that write at 700, carrying a SMALL
          // (100px) shift. 100 < 250 — it would be suppressed if treated as a user
          // fling, but an echo has no momentum to protect, so the re-pin must
          // still correct it (700 -> 800). This pins the programmaticEcho guard.
          let rowOffset = 90
          let phase = 0 // 1: arm the large shift, 2: arm the small shift
          const LARGE = 400
          const SMALL = 100
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return 40000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (phase === 1) {
                phase = 0
                rowOffset += LARGE
                setVersion(v => v + 1)
              }
              else if (phase === 2) {
                phase = 0
                rowOffset += SMALL
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'r', offsetWithinRow: scrollTop - rowOffset }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => rowOffset + a.offsetWithinRow,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Phase 1 (user scroll, large shift -> writes, records programmatic pos).
          phase = 1
          div.setScrollTop(300)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(700)

          // Phase 2 (echo at 700, small shift) — NO await between phases, so the
          // programmatic-position marker hasn't been cleared (it clears on the
          // next rAF/microtask). The small correction must still apply.
          phase = 2
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(800)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops deferred fling drift at stick time so a re-anchor before settle does not inherit it', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = -100
          const ANCHOR_OFFSET = 290
          let armed = false
          let shifted = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return shifted ? 8000 + SHIFT : 8000
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && !shifted) {
                shifted = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? (shifted ? ANCHOR_OFFSET + SHIFT : ANCHOR_OFFSET) + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers -100; arms the settle
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // deferred

          // Stick to bottom: clears the anchor AND drops the deferred drift.
          hook.jumpToBottom()
          expect(div.getScrollTop()).toBe(39500)

          // The user scrolls away from the bottom again BEFORE the settle fires,
          // re-capturing a fresh anchor (no further geometry shift). Without the
          // stick-time reset, the stale -100 would land on THIS anchor at settle.
          armed = false
          div.setScrollTop(20000)
          hook.handlers.onScroll() // re-anchors at 20000, re-arms the settle
          await Promise.resolve()

          vi.advanceTimersByTime(200) // settle fires
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(20000) // no stale drift applied

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops coalesced deferred measurements without a post-momentum write', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const SHIFT = 50
          const ANCHOR_OFFSET = 290
          let armed = false
          // 0 -> no shift; 1 -> first geometry commit (+50); 2 -> second (+100).
          // Both bumps happen inside ONE updateViewport (one scroll event, one
          // anchor capture), simulating two already-committed row-height changes.
          let shiftLevel = 0
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return 8000 + shiftLevel * SHIFT
            },
            geometryVersion: version,
            updateViewport: () => {
              if (armed && shiftLevel < 2) {
                shiftLevel = 1
                setVersion(v => v + 1)
                shiftLevel = 2
                setVersion(v => v + 1)
              }
            },
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'anchored-row'
              ? ANCHOR_OFFSET + shiftLevel * SHIFT + a.offsetWithinRow
              : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          armed = true
          div.setScrollTop(300)
          hook.handlers.onScroll() // two +50 shifts deferred against 'anchored-row'
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300) // deferred, momentum preserved

          // Settle accepts the current visual position instead of applying the
          // +100 correction as a post-momentum write. The accumulator still sees
          // both same-capture measurements, but the user never gets a snap.
          vi.advanceTimersByTime(200)
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops distinct deferrals across captures without a post-momentum write', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Two SEPARATE scroll events at two positions, each capturing a DIFFERENT
          // anchor and deferring its own distinct shift (+50 for row-a at 300, then
          // +30 for row-b at 200). Older code summed those into a later +80 settle
          // write; the current behavior accepts the user's final visual position
          // and re-anchors there instead.
          let phase = 0
          let shiftA = false
          let shiftB = false
          const [version, setVersion] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => {
              version()
              return 8000 + (shiftA ? 50 : 0) + (shiftB ? 30 : 0)
            },
            geometryVersion: version,
            updateViewport: () => {
              if (phase === 1 && !shiftA) {
                shiftA = true
                setVersion(v => v + 1)
              }
              else if (phase === 2 && !shiftB) {
                shiftB = true
                setVersion(v => v + 1)
              }
            },
            anchorAt: () => (phase === 1
              ? { id: 'row-a', offsetWithinRow: 0 }
              : { id: 'row-b', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: a => (a.id === 'row-a'
              ? 300 + (shiftA ? 50 : 0)
              : a.id === 'row-b' ? 200 + (shiftB ? 30 : 0) : a.offsetWithinRow),
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          phase = 1
          div.setScrollTop(300)
          hook.handlers.onScroll() // defers +50 against row-a
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)

          phase = 2
          div.setScrollTop(200) // user flings up; a new anchor (row-b) is captured
          hook.handlers.onScroll() // defers a further +30 against row-b
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(200)

          vi.advanceTimersByTime(200) // settle fires
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(200) // no 200 + (50 + 30) settle snap

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))
})

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

describe('usechatscroll non-wheel scroll direction', () => {
  // A window barely taller than the viewport reads "near" at BOTH edges, so
  // handleScroll dispatches a SINGLE direction from lastScrollDir. handleWheel/
  // handleKeyDown set that for wheel/keys, but a scrollbar drag or touch scroll
  // fires only `scroll` events -- the direction must then be inferred from the
  // scroll-position delta so it pages the way the user actually moved.
  function setupBidirectional() {
    const div = makeFakeScrollDiv()
    // 600px content in a 500px viewport: at scrollTop 50 both isNearTop
    // (50 < 250) and isNearBottom (distFromBottom 50 < 250) hold.
    div.setScrollHeight(600)
    div.setClientHeight(500)
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
    return { div, hook, getOlder: () => olderLoads, getNewer: () => newerLoads }
  }

  it('pages NEWER when a non-wheel scroll moves downward', () =>
    createRoot((dispose) => {
      const { div, hook, getOlder, getNewer } = setupBidirectional()
      // Scrollbar drag downward: scrollTop rises, no wheel event. Without delta
      // inference lastScrollDir stays at its 'older' default and this would page
      // older instead.
      div.setScrollTop(50)
      hook.handlers.onScroll()
      expect(getNewer()).toBe(1)
      expect(getOlder()).toBe(0)
      dispose()
    }))

  it('pages OLDER when a non-wheel scroll moves upward', () =>
    createRoot((dispose) => {
      const { div, hook, getOlder } = setupBidirectional()
      // First move down to establish a non-zero baseline (pages newer)...
      div.setScrollTop(50)
      hook.handlers.onScroll()
      // ...then drag back upward: the negative delta flips the dispatch to older.
      div.setScrollTop(10)
      hook.handlers.onScroll()
      expect(getOlder()).toBe(1)
      dispose()
    }))
})

describe('usechatscroll keyboard navigation', () => {
  function setupHook(opts: {
    hasNewer?: boolean
    hasOlder?: boolean
    onJumpToLatest?: () => void
    onJumpToOldest?: () => void
    onLoadOlderMessages?: () => void
    onLoadNewerMessages?: () => void
  }) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(1000)
    const [messages] = createSignal<AgentChatMessage[]>([])
    const [streamingText] = createSignal('')
    const hook = useChatScroll({
      virtualizer: makeStubVirtualizer(),
      messages,
      streamingText,
      hasNewerMessages: () => opts.hasNewer ?? false,
      hasOlderMessages: () => opts.hasOlder ?? false,
      onJumpToLatest: opts.onJumpToLatest,
      onJumpToOldest: opts.onJumpToOldest,
      onLoadOlderMessages: opts.onLoadOlderMessages,
      onLoadNewerMessages: opts.onLoadNewerMessages,
    })
    hook.attachListRef(div.el)
    return { div, hook }
  }

  function press(hook: ReturnType<typeof setupHook>['hook'], key: string) {
    const ev = new KeyboardEvent('keydown', { key, cancelable: true })
    hook.handlers.onKeyDown(ev)
    return ev
  }

  it('jumps to the live tail on End when windowed away from it', () =>
    createRoot((dispose) => {
      let jumped = false
      const onJumpToLatest = () => {
        jumped = true
      }
      const { hook } = setupHook({ hasNewer: true, onJumpToLatest })
      const ev = press(hook, 'End')
      expect(jumped).toBe(true)
      expect(ev.defaultPrevented).toBe(true)
      dispose()
    }))

  it('jumps to the first message on Home when older history exists', () =>
    createRoot((dispose) => {
      let jumped = false
      const onJumpToOldest = () => {
        jumped = true
      }
      const { hook } = setupHook({ hasOlder: true, onJumpToOldest })
      const ev = press(hook, 'Home')
      expect(jumped).toBe(true)
      expect(ev.defaultPrevented).toBe(true)
      dispose()
    }))

  it('keeps the viewport pinned to the very top after Home when the top rows measure taller', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // 20 rows x 100px = 2000px of content in a 500px viewport, parked mid-list.
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(1000)
          // Mirror ChatView's spacer: div.scrollHeight tracks virt.totalHeight() in a
          // RENDER effect, so it flushes before the geometry re-pin createEffect reads
          // it (exactly the ordering useChatScroll relies on -- see repinToAnchor).
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Home pins to the very top.
          press(hook, 'Home')
          expect(div.getScrollTop()).toBe(0)
          // The programmatic scrollTop=0 write echoes a native scroll event, which
          // re-captures the viewport anchor. It must NOT re-anchor to the viewport
          // MIDPOINT here -- at the top edge the top row must stay pinned to the top.
          hook.handlers.onScroll()

          // The collapsed-until-measured top rows now measure taller: rows 0 and 1
          // grow from 100 to 400 (content above the viewport midpoint grows 600px).
          setRowHeight(0, 400)
          setRowHeight(1, 400)
          await Promise.resolve()
          await Promise.resolve()

          // A midpoint anchor would keep the mid row centered and push scrollTop down
          // ~half a viewport plus the growth; the top pin must hold scrollTop at 0.
          expect(div.getScrollTop()).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps the viewport pinned to the true bottom after End when the bottom rows measure taller', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(1000)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // End sticks to the loaded bottom (2000 - 500 = 1500).
          press(hook, 'End')
          expect(div.getScrollTop()).toBe(1500)
          hook.handlers.onScroll()

          // The bottom rows measure taller after mounting; the view must follow the
          // growing tail to the NEW true bottom, not lag a viewport-fraction behind.
          setRowHeight(18, 400)
          setRowHeight(19, 400)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(total() - 500)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('stays pinned to the top when a window replace invalidates the anchor at the top edge (long-transcript Home)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, replaceWindow, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(0) // parked at the very top, as after a Home jump lands
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // A long (windowed) transcript: Home re-fetches the earliest page and REPLACES
          // the loaded window, so newer history still exists beyond it.
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => true,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // At the top edge the top-row anchor is captured (the state Home lands in).
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(0)

          // The window is replaced under a new generation (jumpToOldest's re-fetch, or a
          // trim), so the anchored top row no longer resolves. The geometry re-pin must
          // recover by re-anchoring to the NEW top row -- NOT the viewport midpoint.
          replaceWindow(Array.from<number>({ length: 20 }).fill(100))
          await Promise.resolve()
          await Promise.resolve()

          // The fresh collapsed-until-measured top rows now measure taller. A midpoint
          // re-anchor would drift the view a viewport-fraction below the top; the top
          // pin must hold scrollTop at 0.
          setRowHeight(0, 400)
          setRowHeight(1, 400)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('stays pinned to the very top when leading collapsed rows measure taller (long-transcript Home)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // Freshly-loaded page: the visible top rows are collapsed-until-measured
          // (height 0) and stack at offset 0; only the rows below carry an estimate.
          const { virt, setRowHeight, total } = makeRowVirtualizer([0, 0, 0, 0, 0, ...Array.from<number>({ length: 15 }).fill(100)])
          div.setClientHeight(500)
          div.setScrollTop(0)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Home jumps to the top and captures the top anchor. With collapsed rows
          // stacked at offset 0, this must pin the FIRST row -- not the last row at
          // offset 0 (the first VISIBLE row), which would be pushed down as the
          // collapsed rows measure and grow.
          press(hook, 'Home')
          expect(div.getScrollTop()).toBe(0)
          // The programmatic scrollTop=0 write echoes a native scroll event.
          hook.handlers.onScroll()

          // The collapsed top rows measure to their real heights (0 -> 100 each).
          setRowHeight(0, 100)
          setRowHeight(1, 100)
          setRowHeight(2, 100)
          setRowHeight(3, 100)
          setRowHeight(4, 100)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('pages down by a page minus the overlap and prevents the native scroll', () =>
    createRoot((dispose) => {
      const { div, hook } = setupHook({})
      div.setScrollTop(0)
      const ev = press(hook, 'PageDown')
      // clientHeight 500 - 48 overlap = 452.
      expect(div.getScrollTop()).toBe(452)
      expect(ev.defaultPrevented).toBe(true)
      dispose()
    }))

  it('pages up by a page minus the overlap', () =>
    createRoot((dispose) => {
      const { div, hook } = setupHook({})
      div.setScrollTop(1000)
      const ev = press(hook, 'PageUp')
      expect(div.getScrollTop()).toBe(548) // 1000 - 452
      expect(ev.defaultPrevented).toBe(true)
      dispose()
    }))

  it('loads older history on PageUp when already clamped at the top', () =>
    createRoot((dispose) => {
      let loadedOlder = 0
      const { div, hook } = setupHook({
        hasOlder: true,
        onLoadOlderMessages: () => { loadedOlder++ },
      })
      // Clamped at the top: scrollBy can't move, so the browser emits no scroll
      // event and edge pagination would never fire. pageScroll must dispatch it.
      div.setScrollTop(0)
      press(hook, 'PageUp')
      expect(div.getScrollTop()).toBe(0)
      expect(loadedOlder).toBe(1)
      dispose()
    }))

  it('loads newer history on PageDown when already clamped at the bottom', () =>
    createRoot((dispose) => {
      let loadedNewer = 0
      const { div, hook } = setupHook({
        hasNewer: true,
        onLoadNewerMessages: () => { loadedNewer++ },
      })
      div.setScrollTop(4500) // clamped at the bottom (5000 - 500)
      press(hook, 'PageDown')
      expect(div.getScrollTop()).toBe(4500)
      expect(loadedNewer).toBe(1)
      dispose()
    }))

  it('does not directly dispatch pagination when the page actually scrolls', () =>
    createRoot((dispose) => {
      let loadedOlder = 0
      const { div, hook } = setupHook({
        hasOlder: true,
        onLoadOlderMessages: () => { loadedOlder++ },
      })
      // Room to move: scrollBy lands at 548 and (in a real browser) the native
      // scroll event owns pagination. pageScroll must NOT also dispatch -- here
      // no scroll event is synthesized, so loadOlder stays 0 (no double-fire).
      div.setScrollTop(1000)
      press(hook, 'PageUp')
      expect(div.getScrollTop()).toBe(548)
      expect(loadedOlder).toBe(0)
      dispose()
    }))

  it('does not page or spuriously dispatch pagination on a 0-height (hidden) container', () =>
    createRoot((dispose) => {
      let loads = 0
      const { div, hook } = setupHook({
        hasOlder: true,
        hasNewer: true,
        onLoadOlderMessages: () => { loads++ },
        onLoadNewerMessages: () => { loads++ },
      })
      // Hidden/collapsed pane: clientHeight 0. The page delta would be 0, scrollBy
      // a no-op, and scrollTop === before -- which WITHOUT the guard fires edge
      // pagination on every key press. The clientHeight===0 guard bails instead.
      div.setClientHeight(0)
      div.setScrollTop(0)
      press(hook, 'PageDown')
      press(hook, 'PageUp')
      expect(div.getScrollTop()).toBe(0)
      expect(loads).toBe(0)
      dispose()
    }))
})

describe('usechatscroll toggle row stability', () => {
  // The reported bug: expanding/collapsing a message row above the viewport midpoint
  // scrolled the row out of position. Root cause -- the geometry re-pin holds whatever
  // row sits at the viewport MIDPOINT, so a height change ABOVE the midpoint pushes the
  // midpoint row down and the re-pin compensates by scrolling, dragging the toggled row
  // with it. anchorRowForResize pins the TOGGLED row's own top instead, so its own
  // resize (which never moves the rows above it) leaves scrollTop untouched.

  it('drifts a row above the midpoint when only the bare midpoint re-pin holds it (the bug)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // 20 rows x 100px = 2000px of content in a 500px viewport, parked mid-list so
          // the viewport top (offset 800) is well below the top edge -- captureAnchor
          // pins the midpoint row, not the top row.
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(800) // viewport [800,1300], midpoint at 1050 (row 10)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park mid-list (the hook auto-sticks to the bottom on mount) and let the
          // scroll event capture the viewport-MIDPOINT anchor (row 10, top 1000).
          div.setScrollTop(800)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // Expand row 8 (top 800, at the viewport TOP -- above the midpoint): +400px.
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()

          // The midpoint re-pin keeps row 10 centered, so the 400px grown above it pushes
          // scrollTop down by 400 -- row 8's top scrolls up off the viewport top. THIS is
          // the drift the fix eliminates (see the next test).
          expect(div.getScrollTop()).toBe(1200)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps a toggled row above the midpoint stationary when anchorRowForResize pins it (the fix)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(800) // same mid-list park as the bug case above
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park mid-list and capture the midpoint anchor, exactly as in the bug case.
          div.setScrollTop(800)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // ChatView calls this the instant the user clicks expand/collapse, BEFORE the
          // height change -- pinning row 8's own top at its current viewport line.
          hook.anchorRowForResize('g0_8')
          // Expand row 8: +400px, the same growth that drifted 400px above.
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()

          // Row 8's top (offset 800) is unchanged by its own growth, so the re-pin holds
          // scrollTop at 800: the row stays exactly where the user clicked.
          expect(div.getScrollTop()).toBe(800)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps a toggled row stationary on COLLAPSE (a shrink above the midpoint)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // Row 8 starts EXPANDED at 500px; collapsing it shrinks it back to 100px.
          const heights = Array.from<number>({ length: 20 }).fill(100)
          heights[8] = 500
          const { virt, setRowHeight, total } = makeRowVirtualizer(heights)
          div.setClientHeight(500)
          div.setScrollTop(800) // viewport top sits at row 8's top (offset 800)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park with the viewport top at row 8's top and capture the midpoint anchor.
          div.setScrollTop(800)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          hook.anchorRowForResize('g0_8')
          setRowHeight(8, 100) // collapse: -400px below the pinned top
          await Promise.resolve()
          await Promise.resolve()

          // The shrink happens BELOW row 8's top, so its top holds and scrollTop stays put.
          expect(div.getScrollTop()).toBe(800)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('holds the toggle-anchor across the re-pin echo instead of reverting to the midpoint', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // The REAL bug (from a production trace): a toggle pins the row's top, but the
          // toggle's own keep-position re-pin writes scrollTop, and the browser echoes a
          // scroll event. handleScroll runs for that echo and -- without the hold -- RE-
          // CAPTURES the viewport-midpoint anchor, discarding the toggle pin. The resize's
          // NEXT phase (estimate -> measured) then re-pins against the midpoint and jumps.
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(800) // viewport [800,1300], midpoint 1050 (row 10)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park mid-list; the scroll event captures the viewport-MIDPOINT anchor (row 10).
          div.setScrollTop(800)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // Toggle row 8 (above the midpoint): pin its top and arm the hold.
          hook.anchorRowForResize('g0_8')

          // The re-pin's own echo scroll event. WITHOUT the hold this re-captures the
          // midpoint anchor; the hold makes handleScroll leave the toggle-anchor intact.
          hook.handlers.onScroll()

          // Two-phase resize (estimate then measured), each echoing another scroll event --
          // exactly the cascade the trace showed. The toggle-anchor must survive all of it.
          setRowHeight(8, 300)
          await Promise.resolve()
          await Promise.resolve()
          hook.handlers.onScroll()
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()

          // Row 8's top (offset 800) never moved, so the view holds. A reverted midpoint
          // anchor would instead have yanked scrollTop down to ~1200 (the reported jump).
          expect(div.getScrollTop()).toBe(800)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('resumes normal midpoint anchoring once the user genuinely scrolls (hold released)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // The hold must not linger: a real wheel gesture releases it so the next scroll
          // event re-captures the live viewport anchor as usual.
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(800)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(800)
          hook.handlers.onScroll()
          hook.anchorRowForResize('g0_8') // arms the hold

          // A genuine wheel gesture: the user takes control, releasing the hold.
          hook.handlers.onWheel({ deltaX: 0, deltaY: 10, ctrlKey: false } as WheelEvent)
          div.setScrollTop(900) // the wheel scrolls the viewport
          hook.handlers.onScroll() // hold released -> re-captures the midpoint anchor (row 11)

          // A row above the new midpoint grows by 400px (a large, structural shift written
          // immediately, past the fling-suppress threshold). The re-pin must keep the NEW
          // MIDPOINT row stationary (900 -> 1300), NOT row 8's top (which the released
          // toggle-anchor would have pinned to a different position).
          setRowHeight(3, 500)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(1300)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does NOT arm the hold when the toggled row is not in the window (re-capture stays live)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // captureRowTopAnchor bails when the row id is not in the offset map, so
          // anchorRowForResize must leave the hold DISARMED -- otherwise a toggle on a
          // trimmed-away row would wedge handleScroll's re-capture off and freeze the anchor
          // at a stale position for the rest of the session.
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(800)
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(800)
          hook.handlers.onScroll() // midpoint anchor at scrollTop 800

          // A toggle on a row that is NOT in the window: the anchor can't be set, so the
          // hold must stay disarmed.
          hook.anchorRowForResize('not-in-window')

          // The viewport moves and a scroll event fires. With the hold disarmed (correct),
          // handleScroll re-captures the midpoint at the NEW position (1200); if the hold
          // had been wrongly armed, it would skip the re-capture and freeze the old anchor.
          div.setScrollTop(1200)
          hook.handlers.onScroll()

          // A large grow above the new midpoint: the re-pin tracks the RE-CAPTURED anchor
          // (row 14 at scrollTop 1200 -> 1600). A frozen stale anchor (row 10) would instead
          // resolve to a no-op and leave scrollTop at 1200.
          setRowHeight(3, 500)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(1600)
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

describe('usechatscroll explicit edge-intent tolerance', () => {
  function setupEdge(scrollTop: number) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(scrollTop)
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
    return { hook, getOlder: () => olderLoads, getNewer: () => newerLoads }
  }

  it('loads older on a wheel-up at a sub-pixel scrollTop (fractional DPI/zoom)', () =>
    createRoot((dispose) => {
      // A re-pin/clamp on a fractional-DPI display can leave scrollTop at 0.5 even
      // when the viewport is visually at the very top. An exact `=== 0` gate would
      // never fire here while the tolerant bottom side does -- the two edges must
      // behave symmetrically.
      const { hook, getOlder } = setupEdge(0.5)
      hook.handlers.onWheel({ deltaX: 0, deltaY: -120, ctrlKey: false } as WheelEvent)
      expect(getOlder()).toBe(1)
      dispose()
    }))

  it('does NOT load older on a wheel-up when scrolled past the edge tolerance', () =>
    createRoot((dispose) => {
      // Well below the top: a wheel-up is ordinary scrolling, not a reach-the-top
      // intent, so it must not page older.
      const { hook, getOlder } = setupEdge(200)
      hook.handlers.onWheel({ deltaX: 0, deltaY: -120, ctrlKey: false } as WheelEvent)
      expect(getOlder()).toBe(0)
      dispose()
    }))

  it('loads newer on a wheel-down at a sub-pixel distance from the bottom', () =>
    createRoot((dispose) => {
      // 1px above the clamped bottom (distFromBottom 1): the tolerant bottom gate
      // still reads this as bottom-intent.
      const { hook, getNewer } = setupEdge(4499)
      hook.handlers.onWheel({ deltaX: 0, deltaY: 120, ctrlKey: false } as WheelEvent)
      expect(getNewer()).toBe(1)
      dispose()
    }))
})

describe('usechatscroll stall indicators', () => {
  // Content 5000 / viewport 500 => max scrollTop 4500: scrollTop 0 is the top edge,
  // 4500 the bottom edge (distFromBottom 0). Fetch flags are signals so a test can
  // flip a fetch mid-flight; load handlers are no-ops (the in-flight guard already
  // suppresses pagination while a fetch flag is set).
  function setupStall(opts: {
    scrollTop: number
    fetchingOlder?: boolean
    fetchingNewer?: boolean
    hasOlder?: boolean
    hasNewer?: boolean
  }) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(opts.scrollTop)
    const [messages] = createSignal<AgentChatMessage[]>([])
    const [streamingText] = createSignal('')
    const [fetchingOlder, setFetchingOlder] = createSignal(opts.fetchingOlder ?? false)
    const [fetchingNewer, setFetchingNewer] = createSignal(opts.fetchingNewer ?? false)
    const hook = useChatScroll({
      virtualizer: makeStubVirtualizer(),
      messages,
      streamingText,
      hasOlderMessages: () => opts.hasOlder ?? true,
      hasNewerMessages: () => opts.hasNewer ?? true,
      fetchingOlder,
      fetchingNewer,
      onLoadOlderMessages: () => {},
      onLoadNewerMessages: () => {},
    })
    hook.attachListRef(div.el)
    return { hook, div, setFetchingOlder, setFetchingNewer }
  }

  it('stalledOlder is TRUE when clamped at the top edge with an older fetch in flight', () =>
    createRoot((dispose) => {
      const { hook } = setupStall({ scrollTop: 0, fetchingOlder: true, hasOlder: true })
      expect(hook.stalledOlder()).toBe(true)
      dispose()
    }))

  it('stalledOlder stays dark for a background older pre-fetch away from the top edge', () =>
    createRoot((dispose) => {
      // scrollTop well inside the buffer: a pre-fetch is in flight but the reader is
      // not waiting at the edge, so no indicator.
      const { hook } = setupStall({ scrollTop: 1000, fetchingOlder: true, hasOlder: true })
      expect(hook.stalledOlder()).toBe(false)
      dispose()
    }))

  it('stalledOlder is dark with no older fetch in flight, and with no older history', () =>
    createRoot((dispose) => {
      expect(setupStall({ scrollTop: 0, fetchingOlder: false, hasOlder: true }).hook.stalledOlder()).toBe(false)
      expect(setupStall({ scrollTop: 0, fetchingOlder: true, hasOlder: false }).hook.stalledOlder()).toBe(false)
      dispose()
    }))

  it('stalledNewer is TRUE when clamped at the bottom edge with a newer fetch in flight', () =>
    createRoot((dispose) => {
      const { hook } = setupStall({ scrollTop: 4500, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(true)
      dispose()
    }))

  it('stalledNewer stays dark for a background newer pre-fetch away from the bottom edge', () =>
    createRoot((dispose) => {
      const { hook } = setupStall({ scrollTop: 3000, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))

  it('stalledNewer keys off distFromBottom, not scrollTop: a newer fetch at the TOP edge is dark', () =>
    createRoot((dispose) => {
      // At the top edge (scrollTop 0) distFromBottom is 4500 -- nowhere near the
      // bottom -- so a newer fetch here is a background pre-fetch, not a stall.
      const { hook } = setupStall({ scrollTop: 0, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))

  it('stalledNewer turns on when a position-only scroll clamps the view at the bottom edge', () =>
    createRoot((dispose) => {
      // Fetch already in flight while the reader is still inside the buffer; the
      // scroll INTO the edge is a position-only move (no fetch-flag change), so the
      // memo must re-measure off scrollTick.
      const { hook, div } = setupStall({ scrollTop: 3000, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(false)
      div.setScrollTop(4500)
      hook.handlers.onScroll()
      expect(hook.stalledNewer()).toBe(true)
      dispose()
    }))

  it('stalledNewer clears when the fetch ends, even without a scroll event', () =>
    createRoot((dispose) => {
      // A newer page that lands while anchored away from the tail grows content
      // BELOW the viewport without moving scrollTop (no scroll event). The fetch
      // flag flipping false must hide the indicator on its own.
      const { hook, setFetchingNewer } = setupStall({ scrollTop: 4500, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(true)
      setFetchingNewer(false)
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))

  it('stalledNewer clears when the list element detaches', () =>
    createRoot((dispose) => {
      // The stall memos read messageListRef (a plain ref they can't track); attach AND
      // detach bump the geometry tick so they re-evaluate. On unmount the indicator must
      // go dark rather than latch at its last value (no element = nothing to stall on).
      const { hook } = setupStall({ scrollTop: 4500, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(true)
      hook.attachListRef(undefined)
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))
})

describe('usechatscroll viewport restore', () => {
  it('jumps to the live tail on restore when the saved anchor was trimmed away', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
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
          }
          const onClearSavedViewportScroll = () => {
            cleared = true
          }
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest,
            savedViewportScroll: saved,
            onClearSavedViewportScroll,
          })
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
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('jumps to the live tail on restore when the saved at-bottom view was windowed away', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved at the IN-MEMORY bottom while still windowed away from the live
          // tail (hasMoreNewer): the in-memory bottom is NOT the real bottom, so
          // restore must re-fetch the tail rather than stick to the stale window.
          const [saved] = createSignal<ChatScrollState | undefined>({
            atBottom: true,
            hasMoreNewer: true,
          })
          let jumped = false
          let cleared = false
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest: () => { jumped = true },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => { cleared = true },
          })
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
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not auto-load older from a passive scroll after a NEAR-TOP anchor restore', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // The saved anchor RESOLVES to a near-top scrollTop (100 < clientHeight/2).
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: restore resolves the anchor to scrollTop 100.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100) // restored near the top

          // A passive scroll event near the top must NOT auto-page older history --
          // the restore landed us here, the user didn't scroll to the top. Without
          // arming the one-shot suppression on the anchor-resolve branch, this would
          // fire loadOlderMessages immediately after restore.
          div.setScrollTop(50)
          hook.handlers.onScroll()
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

  it('still fills the NEWER side from a passive scroll after a near-top restore (suppression gates only older)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // Small window: both edges sit inside the buffer target (3 * clientHeight),
          // so a fill would page BOTH sides -- letting us prove the older side stays
          // suppressed while the newer side keeps loading (the stall fix).
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved anchor resolves NEAR THE TOP (100 < clientHeight/2), arming the
          // one-shot older-load suppression on restore.
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: true,
          })
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 1000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          let newerLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            onLoadNewerMessages: () => { newerLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: restore resolves the anchor to scrollTop 100 and
          // arms the older-load suppression. No fill has run yet.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)
          expect(newerLoads).toBe(0)

          // A passive scroll (to a pixel != the restored 100, so it's not an echo):
          // the older side stays suppressed, but the newer buffer must still top up.
          // Before the fix the whole fill was skipped here and the list stalled short
          // of the live tail until the user scrolled all the way back to the top.
          div.setScrollTop(150)
          hook.handlers.onScroll()
          expect(olderLoads).toBe(0) // older still suppressed
          expect(newerLoads).toBeGreaterThan(0) // newer side keeps loading
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps the post-restore older-suppression through an unrelated (non-restore) resize', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore on show: lands near the top (100 < clientHeight/2), arms the
          // one-shot older-load suppression.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // An UNRELATED, non-restore resize (editor grow / keyboard) must NOT clear the
          // one-shot suppression -- the regression the resize-time clear removal fixes.
          div.setClientHeight(600)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          // A passive scroll still WITHIN the near-top band (50 < 600/2) stays suppressed.
          div.setScrollTop(50)
          hook.handlers.onScroll()
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

  it('resumes older pre-fetch once the user scrolls OUT of the near-top band after a restore', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100,
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore near the top (100), arming the suppression.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // Scroll DOWN out of the near-top band (300 >= clientHeight/2 = 250): the
          // restored landing is abandoned, so older pre-fetch resumes (no fling-up stall).
          div.setScrollTop(300)
          hook.handlers.onScroll()
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

  it('clears the older-suppression when a trim/measure-shrink makes content fit (no resize, no scroll)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // The wedge this guards: a near-top restore arms the one-shot older-load
          // suppression, then a window trim / row re-measure shrinks the content until
          // it fits the viewport -- changing virt.totalHeight()/geometryVersion() but NOT
          // the container box (no resize) and emitting no scroll event. Neither
          // handleResize nor handleScroll runs, so without the geometry-effect clear the
          // suppression sticks forever and older history never background-loads.
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const [total, setTotal] = createSignal(5000)
          const [geom, setGeom] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => total(),
            geometryVersion: () => geom(),
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore on show: lands near the top (100 < clientHeight/2), arming the
          // one-shot suppression. Content is still scrollable (5000 > 500).
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // CONTROL: a window change re-runs the buffer filler. The older side is still
          // suppressed, so it does NOT load older despite a near-top deficit. (We never
          // call onScroll, so the scroll-path clear can't be the thing under test.)
          setMessages(m => [...m, { id: 'a', seq: 1n } as AgentChatMessage])
          await Promise.resolve()
          await Promise.resolve()
          expect(olderLoads).toBe(0)

          // A trim / measurement shrink makes the content fit (scrollHeight <=
          // clientHeight, so maxScrollTop 0) and bumps geometry -- the geometry effect
          // must clear the now-pointless suppression.
          div.setScrollHeight(400)
          setTotal(400)
          setGeom(v => v + 1)
          await Promise.resolve()
          await Promise.resolve()

          // TEST: the next window change runs the filler with the suppression cleared, so
          // older history finally loads. Before the fix it stayed wedged off forever.
          setMessages(m => [...m, { id: 'b', seq: 2n } as AgentChatMessage])
          await Promise.resolve()
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

  it('resumes older pre-fetch when a scrollbar drag reaches the very TOP after a restore (no wheel/key)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100,
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore near the top (100), arming the suppression.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // A scrollbar-thumb DRAG up to the very top -- plain `scroll` events, NOT
          // wheel/key/touch, so it never reaches tryLoadOlderOnExplicitTopIntent. The
          // drag fires a sequence: the first event seeds the upward direction baseline at
          // the restored position; the drag then continues to the very top (0), still
          // WITHIN the near-top band (0 < 250) so the band-exit clear does not apply.
          // Reaching the top WHILE SCROLLING UP must still clear the older-suppression so
          // the filler can page up; without it the reader wedges unable to load older.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(olderLoads).toBe(0) // the in-band seed event must NOT clear yet
          div.setScrollTop(0)
          hook.handlers.onScroll()
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

  it('restores a saved raw scrollTop fallback when no row anchor resolves', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved while scrolled up in an all-hidden window: no anchor, but a raw
          // scrollTop fallback (no virtual spacer there, so no drift to fear).
          const [saved] = createSignal<ChatScrollState | undefined>({
            rawScrollTop: 1200,
            atBottom: false,
            hasMoreNewer: false,
          })
          const hook = useChatScroll({
            // anchorAt / scrollTopForAnchor return null -> the anchor never resolves.
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
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
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('discards a stale raw scrollTop and clamps to the top when virtual rows appeared between save and restore', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved while all-hidden (raw fallback, no anchor). But by restore time
          // the window has virtual content: totalHeight > 0 means a spacer now sits
          // where the raw offset pointed, so 1200 no longer maps to that content.
          const [saved] = createSignal<ChatScrollState | undefined>({
            rawScrollTop: 1200,
            atBottom: false,
            hasMoreNewer: false,
          })
          let olderLoads = 0
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 3000, // virtual rows exist now
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null, // the all-hidden save carried no anchor
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
          })
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
          // The clamp-to-top fallback arms the one-shot auto-load-older suppression,
          // so the restore itself doesn't page older history.
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

  it('pre-fetches older pages AHEAD until the visible buffer above is filled, then stops', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500) // bufferTarget = 3 screens = 1500px; refill watermark = 2.5 screens = 1250px
          let scrollHeight = 700
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(200) // visible buffer ABOVE = 200, well under 1500
          const [total, setTotal] = createSignal(700)
          const virt: ChatScrollVirtualizer = {
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

  it('does not pre-fetch the buffer while the tab is hidden (clientHeight 0)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(0) // hidden/background tab: no laid-out viewport
          div.setScrollHeight(700)
          div.setScrollTop(200) // a deficient above-buffer were the tab visible
          const virt: ChatScrollVirtualizer = {
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
          div.setScrollHeight(700)
          div.setScrollTop(200) // buffer above 200 < 1500 -> deficient, would pre-fetch
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 700,
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
          div.setScrollHeight(700)
          div.setScrollTop(200) // at the bottom (maxScrollTop 200); older buffer deficient
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => 700,
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

describe('usechatscroll overscroll-at-top drag', () => {
  function setupHook(scrollTop: number) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(scrollTop)
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
    return { hook, olderLoads: () => olderLoads }
  }

  const touch = (clientY: number) => ({ touches: [{ clientY }] }) as unknown as TouchEvent
  const pointer = (clientY: number, pointerType = 'touch') =>
    ({ clientY, pointerType }) as unknown as PointerEvent

  it('loads older history on a downward touch drag past the threshold while at the top', () => {
    createRoot((dispose) => {
      const { hook, olderLoads } = setupHook(0)
      hook.handlers.onTouchStart(touch(100))
      hook.handlers.onTouchMove(touch(120)) // +20px > 12px threshold
      expect(olderLoads()).toBe(1)
      dispose()
    })
  })

  it('ignores a sub-threshold drag and a drag when not scrolled to the top', () => {
    createRoot((dispose) => {
      const sub = setupHook(0)
      sub.hook.handlers.onTouchStart(touch(100))
      sub.hook.handlers.onTouchMove(touch(108)) // +8px < threshold
      expect(sub.olderLoads()).toBe(0)

      const notTop = setupHook(300) // scrollTop != 0 -> not at the top
      notTop.hook.handlers.onTouchStart(touch(100))
      notTop.hook.handlers.onTouchMove(touch(140))
      expect(notTop.olderLoads()).toBe(0)
      dispose()
    })
  })

  it('handles non-mouse pointer drags but ignores mouse pointers', () => {
    createRoot((dispose) => {
      const { hook, olderLoads } = setupHook(0)
      // Mouse pointer is ignored (it scrolls via the wheel).
      hook.handlers.onPointerDown(pointer(100, 'mouse'))
      hook.handlers.onPointerMove(pointer(140, 'mouse'))
      expect(olderLoads()).toBe(0)
      // A touch pointer drag past the threshold loads older.
      hook.handlers.onPointerDown(pointer(100))
      hook.handlers.onPointerMove(pointer(140))
      expect(olderLoads()).toBe(1)
      dispose()
    })
  })
})

describe('usechatscroll getScrollState', () => {
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
})

describe('inferScrollDirection', () => {
  it('returns older when scrollTop moved up (toward older history)', () => {
    expect(inferScrollDirection(500, 300)).toBe('older')
  })

  it('returns newer when scrollTop moved down', () => {
    expect(inferScrollDirection(300, 500)).toBe('newer')
  })

  it('returns null when the position did not change (no direction to infer)', () => {
    expect(inferScrollDirection(420, 420)).toBeNull()
  })

  it('treats a one-pixel delta as a direction (scrollbar nudge / momentum tail)', () => {
    expect(inferScrollDirection(100, 101)).toBe('newer')
    expect(inferScrollDirection(100, 99)).toBe('older')
  })
})

describe('usechatscroll down-jump on a small scroll-down', () => {
  // Controllable virtualizer with a single anchored row at `rowOffset`; `prepend`
  // grows the content above it (and the total height) like older history landing.
  function makeAnchorVirt(rowOffset0: number, total0: number) {
    let total = total0
    let rowOffset = rowOffset0
    const [version, setVersion] = createSignal(0)
    const virt: ChatScrollVirtualizer = {
      totalHeight: () => {
        version()
        return total
      },
      geometryVersion: version,
      updateViewport: () => {},
      anchorAt: scrollTop => ({ id: 'row', offsetWithinRow: scrollTop - rowOffset }),
      scrollTopNearAnchor: () => null,
      scrollTopForAnchor: a => rowOffset + a.offsetWithinRow,
    }
    const prepend = (px: number) => {
      rowOffset += px
      total += px
      setVersion(v => v + 1)
    }
    return { virt, prepend }
  }

  it('re-engages follow when a small down-scroll lands within the 32px band of the live tail (S7)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          // Short window (the hidden-heavy case): only 100px of scroll range, so the
          // whole content sits within ~a viewport. Bottom is at scrollTop 100.
          div.setClientHeight(500)
          div.setScrollHeight(600)
          const ctrl = makeAnchorVirt(0, 600)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: ctrl.virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Anchor by scrolling to the top.
          div.setScrollTop(0)
          hook.handlers.onScroll()
          // Scroll DOWN a bit: distFromBottom = 600 - 80 - 500 = 20 -- inside the 32px
          // sticky band (not the 1px clamped bottom at scrollTop 100). With the unified
          // band, `atBottom` implies following, so this re-engages tail-follow (the
          // reader sitting slightly above the bottom is treated as following). Gated on
          // the LIVE tail (no hasNewerMessages), which holds for this empty list.
          div.setScrollTop(80)
          hook.handlers.onScroll()

          // 200px of older content lands above. Because the small down-scroll re-engaged
          // FOLLOW, the grow sticks to the new live bottom (clamped maxScrollTop 300),
          // NOT the anchored row's +200 track (280).
          div.setScrollHeight(800)
          ctrl.prepend(200)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(300)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not re-engage tail-follow (no down-chase) at a windowed-away loaded bottom', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(1000)
          div.setScrollTop(500) // at the bottom (the live tail, initially)

          // Growable total + a real anchor, so dropping follow yields ANCHORED (not
          // following). scrollTopForAnchor returns the captured midpoint, so the re-pin is
          // a no-op when newer appends BELOW the anchor -- isolating the follow path.
          const [total, setTotal] = createSignal(1000)
          const virt: ChatScrollVirtualizer = {
            totalHeight: () => total(),
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'row', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 850,
          }
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [streamingText] = createSignal('')
          const [hasNewer, setHasNewer] = createSignal(false)
          const [agentStatus, setAgentStatus] = createSignal<AgentStatus | undefined>(AgentStatus.ACTIVE)
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            agentStatus,
            hasNewerMessages: hasNewer,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Establish a sticky record at the LIVE tail: a tracked-input change plus a
          // grown scrollHeight makes the auto-scroll effect stick to the new bottom.
          div.setScrollHeight(1100)
          setAgentStatus(AgentStatus.STARTING)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(600) // stuck; the sticky record is now 600 / 1100

          // Window AWAY from the live tail, then scroll to the loaded clamped bottom.
          // With more newer to load, follow-tail must NOT re-engage -- stay anchored.
          setHasNewer(true)
          div.setScrollTop(600)
          hook.handlers.onScroll()

          // A newer page appends BELOW: scrollHeight + total grow. A chasing re-stick
          // (the bug) would snap scrollTop to the new bottom (1300); staying anchored
          // leaves it where the user stopped.
          div.setScrollHeight(1800)
          setTotal(1800)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(600) // no down-chase past where the user stopped
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

describe('computekeepnewest', () => {
  const msg = (id: string, seq: bigint) => ({ id, seq } as AgentChatMessage)
  const anchor = (id: string) => ({ id, offsetWithinRow: 0 })
  const msgs = [msg('m1', 1n), msg('m2', 2n), msg('m3', 3n), msg('m4', 4n)]

  it('keeps 0 when following the tail (no anchor), so the store applies the normal cap', () => {
    expect(computeKeepNewest(msgs, null, -1)).toBe(0)
  })

  it('keeps the whole window when the anchor is set but unresolvable (displaced / empty)', () => {
    expect(computeKeepNewest(msgs, anchor('gone'), -1)).toBe(msgs.length)
  })

  it('keeps the server rows from the anchor down to the tail', () => {
    // Anchor at index 1 (m2): keep m2, m3, m4 -> 3 rows.
    expect(computeKeepNewest(msgs, anchor('m2'), 1)).toBe(3)
  })

  it('excludes trailing optimistic locals (seq 0n) from the kept-server count', () => {
    // m1, m2 server + two trailing locals; anchor at m1 (index 0). The store caps
    // server messages only, so the locals must NOT inflate the kept count.
    const withLocals = [msg('m1', 1n), msg('m2', 2n), msg('local-a', 0n), msg('local-b', 0n)]
    expect(computeKeepNewest(withLocals, anchor('m1'), 0)).toBe(2)
  })
})

describe('computebufferawarekeepnewest', () => {
  const msg = (id: string, seq: bigint) => ({ id, seq } as AgentChatMessage)
  const anchor = (id: string) => ({ id, offsetWithinRow: 0 })
  const msgs = [msg('m1', 1n), msg('m2', 2n), msg('m3', 3n), msg('m4', 4n)]
  const mustNotResolve = (): null => {
    throw new Error('anchorAt should not be called')
  }

  it('keeps the lean base cap (0) for a hidden tab (clientHeight 0), without resolving an anchor', () => {
    expect(computeBufferAwareKeepNewest(msgs, 1000, 0, 200, mustNotResolve)).toBe(0)
  })

  it('keeps the whole window when less than a buffer of visible content sits above the top', () => {
    // scrollTop 150 - bufferTargetPx 200 = -50 <= 0 -> keep all (never reap surfaced content).
    expect(computeBufferAwareKeepNewest(msgs, 150, 500, 200, mustNotResolve)).toBe(msgs.length)
  })

  it('delegates to computeKeepNewest from the resolved buffer-top anchor', () => {
    // bufTop = 1000 - 200 = 800; anchorAt resolves to m2 (index 1) -> keep m2..m4 = 3.
    let askedBufTop = -1
    const got = computeBufferAwareKeepNewest(msgs, 1000, 500, 200, (bufTop) => {
      askedBufTop = bufTop
      return anchor('m2')
    })
    expect(askedBufTop).toBe(800)
    expect(got).toBe(3)
  })

  it('keeps the whole window when the buffer top is unresolvable (anchorAt null)', () => {
    expect(computeBufferAwareKeepNewest(msgs, 1000, 500, 200, () => null)).toBe(msgs.length)
  })
})

describe('createscrollinput', () => {
  function fakeEl(opts: { clientHeight?: number, scrollTop?: number, clamp?: boolean } = {}) {
    const el = {
      clientHeight: opts.clientHeight ?? 500,
      scrollTop: opts.scrollTop ?? 1000,
      // clamp:true models an at-edge container where scrollBy can't move.
      scrollBy: ({ top }: { top: number }) => {
        if (!opts.clamp)
          el.scrollTop += top
      },
    }
    return el as unknown as HTMLDivElement
  }

  function setup(el: HTMLDivElement | undefined) {
    const calls = {
      lastScrollDir: [] as Array<'older' | 'newer'>,
      discretePageTarget: [] as Array<number | null>,
      tryOlder: 0,
      tryNewer: 0,
      forceBottom: 0,
      cancelPending: 0,
      captureAnchor: 0,
      captureTopAnchor: 0,
    }
    const input = createScrollInput(
      makeScrollContext({ getEl: () => el }),
      {
        captureAnchor: () => { calls.captureAnchor++ },
        captureTopAnchor: () => { calls.captureTopAnchor++ },
        checkAtBottom: () => {},
        forceScrollToBottom: () => { calls.forceBottom++ },
        cancelScrollAnimation: () => {},
        cancelPendingScroll: () => { calls.cancelPending++ },
        tryLoadOlderOnExplicitTopIntent: () => { calls.tryOlder++ },
        tryLoadNewerOnExplicitBottomIntent: () => { calls.tryNewer++ },
        setLastScrollDir: dir => calls.lastScrollDir.push(dir),
        setDiscretePageTarget: target => calls.discretePageTarget.push(target),
        hasOlderMessages: () => false,
        onJumpToOldest: undefined,
      },
    )
    return { input, calls }
  }

  it('pageScroll mid-list records the un-clamped target, scrolls, and does NOT directly load', () => {
    const el = fakeEl({ clientHeight: 500, scrollTop: 1000 })
    const { input, calls } = setup(el)
    input.pageScroll(1)
    // delta = max(500-48, 250) = 452; target = 1000 + 452 = 1452.
    expect(calls.discretePageTarget).toEqual([1452])
    expect(el.scrollTop).toBe(1452) // moved -> the native scroll event owns the fill
    expect(calls.tryNewer).toBe(0)
    expect(calls.tryOlder).toBe(0)
  })

  it('pageScroll at an edge (no movement) clears the target and pages via the explicit-intent loader', () => {
    const el = fakeEl({ clientHeight: 500, scrollTop: 0, clamp: true })
    const { input, calls } = setup(el)
    input.pageScroll(-1)
    // target -452 set, then cleared to null when scrollBy couldn't move. The direction
    // routes to the un-paused older-intent loader (NOT the pause-gated buffer filler).
    expect(calls.discretePageTarget).toEqual([-452, null])
    expect(calls.tryOlder).toBe(1)
    expect(calls.tryNewer).toBe(0)
  })

  it('pageScroll at the bottom edge pages newer via the explicit-intent loader', () => {
    const el = fakeEl({ clientHeight: 500, scrollTop: 9999, clamp: true })
    const { input, calls } = setup(el)
    input.pageScroll(1)
    expect(calls.discretePageTarget).toEqual([10451, null])
    expect(calls.tryNewer).toBe(1)
    expect(calls.tryOlder).toBe(0)
  })

  it('pageScroll is a no-op on a 0-height (hidden) container', () => {
    const { input, calls } = setup(fakeEl({ clientHeight: 0 }))
    input.pageScroll(1)
    expect(calls.discretePageTarget).toEqual([])
    expect(calls.tryNewer).toBe(0)
    expect(calls.tryOlder).toBe(0)
  })

  it('handleKeyDown PageDown pages newer and prevents default', () => {
    const { input, calls } = setup(fakeEl())
    const preventDefault = vi.fn()
    input.handleKeyDown({ key: 'PageDown', preventDefault } as unknown as KeyboardEvent)
    expect(preventDefault).toHaveBeenCalled()
    expect(calls.lastScrollDir).toEqual(['newer'])
    expect(calls.discretePageTarget.length).toBeGreaterThan(0) // pageScroll ran
  })

  it('handleKeyDown End forces scroll to the live tail', () => {
    const { input, calls } = setup(fakeEl())
    input.handleKeyDown({ key: 'End', preventDefault: () => {} } as KeyboardEvent)
    expect(calls.forceBottom).toBe(1)
    expect(calls.lastScrollDir).toEqual(['newer'])
  })

  it('handleKeyDown Home pins to the viewport-top anchor', () => {
    const { input, calls } = setup(fakeEl())
    input.handleKeyDown({ key: 'Home', preventDefault: () => {} } as KeyboardEvent)
    expect(calls.captureTopAnchor).toBe(1)
    expect(calls.captureAnchor).toBe(0)
    expect(calls.lastScrollDir).toEqual(['older'])
  })

  it('handleKeyDown ignores modifier-chorded keys', () => {
    const { input, calls } = setup(fakeEl())
    input.handleKeyDown({ key: 'PageDown', metaKey: true, preventDefault: () => {} } as KeyboardEvent)
    expect(calls.lastScrollDir).toEqual([])
    expect(calls.discretePageTarget).toEqual([])
  })

  it('handleWheel records the direction and triggers the edge-intent load by deltaY sign', () => {
    const { input, calls } = setup(fakeEl())
    input.handleWheel({ deltaY: -10, deltaX: 0 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual(['older'])
    expect(calls.tryOlder).toBe(1)
    input.handleWheel({ deltaY: 10, deltaX: 0 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual(['older', 'newer'])
    expect(calls.tryNewer).toBe(1)
  })

  it('handleWheel cancels the pending settle on a no-movement (0,0) event', () => {
    const { input, calls } = setup(fakeEl())
    input.handleWheel({ deltaY: 0, deltaX: 0 } as WheelEvent)
    expect(calls.cancelPending).toBe(1)
    expect(calls.lastScrollDir).toEqual([])
  })

  it('handleWheel ignores a horizontal-dominant swipe whose small deltaY leaks (no spurious intent)', () => {
    const { input, calls } = setup(fakeEl())
    // A sideways trackpad swipe: large horizontal delta, tiny vertical leak. It must
    // NOT fire an edge-intent load or set lastScrollDir (which would mis-steer the
    // buffer filler), and is not a (0,0) momentum-cancel either.
    input.handleWheel({ deltaY: -3, deltaX: 80 } as WheelEvent)
    input.handleWheel({ deltaY: 2, deltaX: -80 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual([])
    expect(calls.tryOlder).toBe(0)
    expect(calls.tryNewer).toBe(0)
    expect(calls.cancelPending).toBe(0)
    // A vertical-dominant diagonal still counts as intent.
    input.handleWheel({ deltaY: -80, deltaX: 3 } as WheelEvent)
    expect(calls.lastScrollDir).toEqual(['older'])
    expect(calls.tryOlder).toBe(1)
  })
})
