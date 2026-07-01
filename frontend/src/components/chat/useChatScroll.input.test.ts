import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRenderEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'

import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeRowVirtualizer, makeStubVirtualizer } from './useChatScroll.testkit'

installScrollTestEnv()

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
