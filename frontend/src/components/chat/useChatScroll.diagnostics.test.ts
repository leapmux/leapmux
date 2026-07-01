import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'

import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeGrowableVirtualizer, makeStubVirtualizer } from './useChatScroll.testkit'

installScrollTestEnv()

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

describe('usechatscroll scroll-anomaly warnings', () => {
  // A controllable virtualizer with a single anchored row whose content-Y top is
  // `rowTop`. `moveRowTop` shifts that row and bumps geometryVersion, driving the real
  // geometry re-pin (repinToAnchor) -- so a re-pin can be made to clamp against an edge.
  // anchorAt/scrollTopForAnchor round-trip (offsetWithinRow = y - rowTop), mirroring the
  // production offset map.
  function makeAnchorVirtualizer(initialRowTop: number) {
    let rowTop = initialRowTop
    const [version, setVersion] = createSignal(0)
    const virt: ChatScrollVirtualizer = {
      totalHeight: () => 100000,
      geometryVersion: version,
      updateViewport: () => {},
      anchorAt: (scrollTop: number): ScrollAnchor => ({ id: 'row', offsetWithinRow: scrollTop - rowTop }),
      scrollTopNearAnchor: () => null,
      scrollTopForAnchor: (a: ScrollAnchor): number => rowTop + (a.offsetWithinRow ?? 0),
    }
    return {
      virt,
      moveRowTop: (px: number) => {
        rowTop += px
        setVersion(v => v + 1)
      },
    }
  }

  it('warns when a keep-position re-pin clamps at a loaded edge while more older history exists', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)
          const { virt, moveRowTop } = makeAnchorVirtualizer(90)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [hasOlder] = createSignal<boolean | undefined>(true)
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText, hasOlderMessages: hasOlder })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Capture the viewport-midpoint anchor at scrollTop 100 (row top 90).
          div.setScrollTop(100)
          hook.handlers.onScroll()
          // The stub mounts already tail-following (atBottom defaults true), so this first
          // raw onScroll -- with no input driving it -- reads as a small jump against the
          // stale baseline. That's a harness artifact (in the app an upward scroll carries
          // wheel/touch input and is excluded); clear it so we assert only the clamp below.
          warn.mockClear()

          // Content above the anchor is removed (its top drops 90 -> -210): the ideal
          // keep-position target goes negative and clamps to 0. The anchored row jumps
          // ~200px up even though older history is still loadable -- an avoidable clamp.
          moveRowTop(-300)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(0)
          expect(warn).toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('anchor re-pin clamped'),
            expect.objectContaining({ clampedAt: 'top', clampPx: 200 }),
          )
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('stays silent when the same clamp happens at a genuinely exhausted top edge (no older history)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(100)
          const { virt, moveRowTop } = makeAnchorVirtualizer(90)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [hasOlder] = createSignal<boolean | undefined>(false) // top edge exhausted
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText, hasOlderMessages: hasOlder })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(100)
          hook.handlers.onScroll()
          // Clear the harness artifact (see the sibling test): the mount-time tail-follow
          // leaves the baseline stale, so this first input-less onScroll reads as a small
          // jump. We assert the clamp path stays silent below.
          warn.mockClear()
          moveRowTop(-300)
          await Promise.resolve()
          await Promise.resolve()

          // The clamp still happens (the row must move -- nothing left to reveal), but
          // it is expected, so no WARN fires.
          expect(div.getScrollTop()).toBe(0)
          expect(warn).not.toHaveBeenCalled()
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('warns on an unexplained large scroll jump with no known cause', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(0)
          hook.handlers.onScroll() // seed lastScrollTop = 0
          warn.mockClear()

          // The viewport teleports 3000px between two scroll events with no wheel/touch,
          // no drag, no keyboard page, and no programmatic write -- an unexplained jump.
          div.setScrollTop(3000)
          hook.handlers.onScroll()

          expect(warn).toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('unexpected scroll jump'),
            // The attribution marks this as a cold teleport: no fling was in progress on the
            // prior event (a jump from rest), which is exactly what should still surface. The
            // timing fields are wired -- velocity idle (speed 0) and the inter-event gap present.
            expect.objectContaining({
              deltaFromLast: 3000,
              wasActivelyFlinging: false,
              speedPxPerMs: 0,
              msSinceLastScrollEvent: expect.any(Number),
            }),
          )
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('warns on a small unexplained jump (~40px) that the old 0.3-screen floor would have missed', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(1000)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed the baseline mid-list, well clear of the bottom so the tail-follow
          // exclusion doesn't apply.
          div.setScrollTop(1000)
          hook.handlers.onScroll()
          warn.mockClear()

          // A 40px jump with no input / echo / page. It is below the OLD 0.3-screen floor
          // (~150px on this 500px pane), so it used to slip through; the 32px absolute
          // floor now surfaces it.
          div.setScrollTop(1040)
          hook.handlers.onScroll()

          expect(warn).toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('unexpected scroll jump'),
            expect.objectContaining({ deltaFromLast: 40 }),
          )
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not WARN on a large jump that follows a wheel gesture (a fling has recent momentum input)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(0)
          hook.handlers.onScroll() // seed
          hook.handlers.onWheel({ deltaY: -30, deltaX: 0, ctrlKey: false } as WheelEvent) // marks momentum
          warn.mockClear()

          div.setScrollTop(3000)
          hook.handlers.onScroll()

          expect(warn).not.toHaveBeenCalled()
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not WARN on a trackpad momentum coast that outlives the 750ms input grace', () =>
    new Promise<void>((resolve, reject) => {
      // A macOS trackpad fling: the finger-on wheel event marks momentum input, then the OS
      // drives the coast by firing bare `scroll` events (no fresh vertical wheel events). Past
      // the 750ms MOMENTUM_INPUT_GRACE_MS the input grace lapses -- but the velocity tracker,
      // fed by the scroll events themselves, still reports an active fling, so the coast is
      // excused. Without that signal, the still-moving momentum reads as an unexplained jump.
      vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(40000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Finger-on: a wheel event marks momentum, and the fling begins.
          div.setScrollTop(0)
          hook.handlers.onScroll() // seed the velocity + direction baseline
          hook.handlers.onWheel({ deltaY: 200, deltaX: 0, ctrlKey: false } as WheelEvent)
          // The coast: bare scroll events every ~frame, ~120px each (7.5px/ms, well over the
          // 1px/ms fling threshold), driven by the OS -- NO further wheel events. Run well
          // past the 750ms grace so hasRecentMomentumInput() has lapsed by the end.
          let top = 0
          for (let t = 0; t < 900; t += 16) {
            vi.advanceTimersByTime(16)
            top += 120
            div.setScrollTop(top)
            hook.handlers.onScroll()
          }
          warn.mockClear()

          // One more coast frame, now firmly past the input grace. The velocity tracker still
          // sees an active fling (fresh, fast samples), so this must NOT warn.
          vi.advanceTimersByTime(16)
          top += 120
          div.setScrollTop(top)
          hook.handlers.onScroll()

          expect(warn).not.toHaveBeenCalled()
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

  it('warns "slow scroll phase" when a refreshViewport render cascade blows the frame budget', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        // A controllable clock so a stubbed updateViewport can "block the main thread" for a
        // deterministic duration (monotonicNow reads performance.now).
        let clock = 1000
        const nowSpy = vi.spyOn(performance, 'now').mockImplementation(() => clock)
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // The scroll-triggered render cascade stalls the main thread 60ms -- over the 50ms
          // budget -- simulated by advancing the clock inside updateViewport (the synchronous
          // mount of entering rows + the premeasure computed run here in the real code).
          const virt: ChatScrollVirtualizer = {
            ...makeStubVirtualizer(),
            updateViewport: () => { clock += 60 },
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          warn.mockClear()

          // A sub-32px scroll: refreshViewport still runs, but the delta is below the teleport
          // floor, so only the phase-stall warn can fire (isolating this instrumentation).
          div.setScrollTop(20)
          hook.handlers.onScroll()

          expect(warn).toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('slow scroll phase'),
            expect.objectContaining({ phase: 'refreshViewport', ms: 60 }),
          )
          nowSpy.mockRestore()
          dispose()
          resolve()
        }
        catch (e) {
          nowSpy.mockRestore()
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not WARN when a large scroll phase stays within budget', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        let clock = 1000
        const nowSpy = vi.spyOn(performance, 'now').mockImplementation(() => clock)
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // A normal (fast, sub-budget) render cascade: 10ms, under the 50ms threshold.
          const virt: ChatScrollVirtualizer = {
            ...makeStubVirtualizer(),
            updateViewport: () => { clock += 10 },
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          warn.mockClear()

          div.setScrollTop(20)
          hook.handlers.onScroll()

          expect(warn).not.toHaveBeenCalled()
          nowSpy.mockRestore()
          dispose()
          resolve()
        }
        catch (e) {
          nowSpy.mockRestore()
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not WARN when a large jump is our own programmatic write echoing back', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(0)
          hook.handlers.onScroll() // seed
          warn.mockClear()

          // A programmatic jump-to-bottom writes scrollTop to the clamped bottom (4500)
          // and marks the guard; the browser's follow-up scroll event is our own echo.
          hook.forceScrollToBottom()
          expect(div.getScrollTop()).toBe(4500)
          hook.handlers.onScroll()

          expect(warn).not.toHaveBeenCalled()
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not warn when a keep-position re-pin echo arrives after the guard marker TTL (delayed echo)', () =>
    new Promise<void>((resolve, reject) => {
      // Fake performance.now so the programmatic-guard's echo marker can be aged past its
      // ~150ms TTL between the re-pin write and the browser delivering its echo scroll
      // event -- the busy-main-thread case (a long older prepend re-measuring a 1200-row
      // list) the field report hit: fetchingOlder true, a 548px UP move, markers empty.
      vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(40000)
          div.setClientHeight(500)
          div.setScrollTop(1000)
          const { virt, moveRowTop } = makeAnchorVirtualizer(90)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [hasOlder] = createSignal<boolean | undefined>(true)
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText, hasOlderMessages: hasOlder })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // User parks mid-list; capture the viewport-midpoint anchor.
          div.setScrollTop(1000)
          hook.handlers.onScroll()

          // A row above the anchor measures shorter: the keep-position re-pin writes
          // scrollTop 1000 -> 700 to hold the anchored row stationary (a 300px
          // programmatic move -- below clientHeight, so the stale-native path is NOT
          // armed and can't account for the later echo).
          moveRowTop(-300)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(700)
          warn.mockClear()

          // The browser delivers the re-pin's echo scroll event LATE -- past the marker
          // TTL, so the guard no longer recognizes it. It is still OUR own move (the
          // content stayed visually stationary), not an unexplained user jump, so it
          // must not warn.
          vi.advanceTimersByTime(200)
          hook.handlers.onScroll()
          expect(warn).not.toHaveBeenCalled()

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

  it('does not warn when a content shrink clamps scrollTop down to the new bottom (following the tail)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5856)
          div.setClientHeight(733)
          div.setScrollTop(5123) // at the bottom (maxScrollTop 5856 - 733)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [hasOlder] = createSignal<boolean | undefined>(true)
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages, streamingText, hasOlderMessages: hasOlder })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed the baseline at the tail (as a live-tail restick's echo would): the
          // direction baseline becomes 5123.
          hook.handlers.onScroll()
          warn.mockClear()

          // Content shrinks (a streaming block finalizes / a row re-measures shorter):
          // scrollHeight drops, so maxScrollTop (4247) falls below the pinned position and
          // the browser force-clamps scrollTop 5123 -> 4247, firing a scroll event. That
          // is the tail following a shrink, not an unexplained user jump.
          div.setScrollHeight(4980)
          expect(div.getScrollTop()).toBe(4247) // browser clamped to the new bottom
          hook.handlers.onScroll()

          expect(warn).not.toHaveBeenCalled()
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not warn when a large tail-follow restick echo is delivered after the guard marker TTL', () =>
    new Promise<void>((resolve, reject) => {
      // Symmetric grow-side of the delayed-echo case: a large one-step growth while
      // following the tail re-sticks to a much higher bottom, and the busy main thread
      // delivers that restick's echo past the marker TTL.
      vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Follow the tail: sticks scrollTop 0 -> 500 (bottom), which syncs the baseline
          // to 500 and seeds the sticky record.
          hook.forceScrollToBottom()
          expect(div.getScrollTop()).toBe(500)
          warn.mockClear()

          // A large block appears at once (800px): the geometry effect re-sticks scrollTop
          // 500 -> 1300 (the new bottom).
          div.setScrollHeight(1800)
          setTotal(1800)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(1300)

          // The browser delivers the restick's echo LATE, past the marker TTL. It is our
          // own tail-follow move, not an unexplained user jump, so it must not warn.
          vi.advanceTimersByTime(200)
          hook.handlers.onScroll()
          expect(warn).not.toHaveBeenCalled()

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

  // A virtualizer whose anchored row grows `shift` px taller the first time updateViewport
  // runs (a late estimate->measured correction), so a scroll event's own refreshViewport
  // triggers the keep-position re-pin to ABSORB the correction -- the on-screen content
  // shift with no scroll event that Detector C surfaces. Mirrors the sibling absorb test.
  function makeAbsorbingVirtualizer(shift: number) {
    const ANCHOR_OFFSET = 290
    let armed = false
    let shifted = false
    const [version, setVersion] = createSignal(0)
    const virt: ChatScrollVirtualizer = {
      totalHeight: () => {
        version()
        return shifted ? 8000 + shift : 8000
      },
      geometryVersion: version,
      updateViewport: () => {
        if (armed && !shifted) {
          shifted = true
          setVersion(v => v + 1)
        }
      },
      anchorAt: scrollTop => (shifted
        ? { id: 'tall-row', offsetWithinRow: scrollTop }
        : { id: 'anchored-row', offsetWithinRow: scrollTop - ANCHOR_OFFSET }),
      scrollTopNearAnchor: () => null,
      scrollTopForAnchor: a => (a.id === 'anchored-row'
        ? (shifted ? ANCHOR_OFFSET + shift : ANCHOR_OFFSET) + a.offsetWithinRow
        : a.offsetWithinRow),
    }
    return {
      virt,
      arm: () => {
        armed = true
      },
    }
  }

  it('warns when a slow-scroll estimate correction is absorbed as anchor drift (Detector C)', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const { virt, arm } = makeAbsorbingVirtualizer(100) // a 100px correction
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed velocity at a slow cadence.
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          warn.mockClear()

          // 100ms later, a 10px nudge = 0.1 px/ms (slow, not a fling). The row above
          // measures 100px taller during this event; the re-pin ABSORBS the correction
          // (re-anchors instead of snapping back), leaving content shifted 100px on-screen
          // with no scroll event -- exactly what Detector C exists to catch.
          vi.advanceTimersByTime(100)
          arm()
          div.setScrollTop(310)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(310) // absorbed, not corrected
          expect(warn).toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('anchored content drifted'),
            expect.objectContaining({ reason: 'absorbed', residualPx: 100 }),
          )
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

  it('does not warn on an absorbed correction below the drift floor (~10px)', () =>
    new Promise<void>((resolve, reject) => {
      vi.useFakeTimers({ toFake: ['performance', 'setTimeout', 'clearTimeout'] })
      createRoot(async (dispose) => {
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const { virt, arm } = makeAbsorbingVirtualizer(10) // a 10px correction, below the floor
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()
          warn.mockClear()

          vi.advanceTimersByTime(100)
          arm()
          div.setScrollTop(310)
          hook.handlers.onScroll()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(310) // still absorbed
          expect(warn).not.toHaveBeenCalled() // 10px < the 16px drift floor
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
})
