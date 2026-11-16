import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRenderEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { inRoot } from '~/test-support/async'

import { useChatScroll } from './useChatScroll'
import { deferredJump, installScrollTestEnv, makeFakeScrollDiv, makeRowVirtualizer, makeStubVirtualizer, mountScrollHarness, mountWithPendingJump, rowMessages, rowTargetAt, settleJump, virtualizerNoOps, withContentAnchors, withStaticAdmission } from './useChatScroll.testkit'

installScrollTestEnv()

describe('useChatScroll non-wheel scroll direction', () => {
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
    let olderLoads = 0
    let newerLoads = 0
    const hook = useChatScroll(withStaticAdmission({
      virtualizer: makeStubVirtualizer(),
      messages,
      hasOlderMessages: () => true,
      hasNewerMessages: () => true,
      onLoadOlderMessages: () => { olderLoads++ },
      onLoadNewerMessages: () => { newerLoads++ },
    }))
    hook.attachListRef(div.el)
    return {
      div,
      hook,
      getOlder: () => olderLoads,
      getNewer: () => newerLoads,
    }
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

describe('useChatScroll keyboard navigation', () => {
  function setupHook(opts: {
    hasNewer?: boolean
    hasOlder?: boolean
    onJumpToLatest?: (signal?: AbortSignal) => Promise<boolean>
    onJumpToOldest?: (signal?: AbortSignal) => Promise<boolean>
    onLoadOlderMessages?: () => void
    onLoadNewerMessages?: () => void
  }) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(1000)
    const [messages] = createSignal<AgentChatMessage[]>([])
    const hook = useChatScroll(withStaticAdmission({
      virtualizer: makeStubVirtualizer(),
      messages,
      hasNewerMessages: () => opts.hasNewer ?? false,
      hasOlderMessages: () => opts.hasOlder ?? false,
      // Each callback is omitted (not undefined) when the case states none.
      ...(opts.onJumpToLatest !== undefined
        ? { onJumpToLatest: opts.onJumpToLatest }
        : {}),
      ...(opts.onJumpToOldest !== undefined
        ? { onJumpToOldest: opts.onJumpToOldest }
        : {}),
      ...(opts.onLoadOlderMessages !== undefined
        ? { onLoadOlderMessages: opts.onLoadOlderMessages }
        : {}),
      ...(opts.onLoadNewerMessages !== undefined
        ? { onLoadNewerMessages: opts.onLoadNewerMessages }
        : {}),
    }))
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
        return Promise.resolve(true)
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
        return Promise.resolve(true)
      }
      const { hook } = setupHook({ hasOlder: true, onJumpToOldest })
      const ev = press(hook, 'Home')
      expect(jumped).toBe(true)
      expect(ev.defaultPrevented).toBe(true)
      dispose()
    }))

  it('keeps the viewport pinned to the very top after Home when the top rows measure taller', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      // 20 rows x 100px = 2000px of content in a 500px viewport, parked mid-list.
      const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
      div.setClientHeight(500)
      div.setScrollTop(1000)
      // Mirror ChatView's spacer: div.scrollHeight tracks virt.totalHeight() in a
      // RENDER effect, so it flushes before the geometry createEffect reads it
      // (exactly the ordering useChatScroll relies on).
      createRenderEffect(() => div.setScrollHeight(total()))
      const [messages] = createSignal<AgentChatMessage[]>([])
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        hasNewerMessages: () => false,
        hasOlderMessages: () => false,
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Home pins to the very top.
      press(hook, 'Home')
      expect(div.getScrollTop()).toBe(0)
      // The scrollTop=0 placement echoes a native scroll event. It is an own write,
      // so it moves nothing -- at the top edge the top row must stay at the top.
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
    }))

  it('keeps the viewport pinned to the true bottom after End when the bottom rows measure taller', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
      div.setClientHeight(500)
      div.setScrollTop(1000)
      createRenderEffect(() => div.setScrollHeight(total()))
      const [messages] = createSignal<AgentChatMessage[]>([])
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        hasNewerMessages: () => false,
        hasOlderMessages: () => false,
      }))
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
    }))

  it('stays pinned to the top when a window replace invalidates the anchor at the top edge (long-transcript Home)', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      const { virt, setRowHeight, replaceWindow, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
      div.setClientHeight(500)
      div.setScrollTop(0) // parked at the very top, as after a Home jump lands
      createRenderEffect(() => div.setScrollHeight(total()))
      const [messages] = createSignal<AgentChatMessage[]>([])
      // A long (windowed) transcript: Home re-fetches the earliest page and REPLACES
      // the loaded window, so newer history still exists beyond it.
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        hasNewerMessages: () => true,
        hasOlderMessages: () => false,
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // At the top edge the top-row anchor is captured (the state Home lands in).
      hook.handlers.onScroll()
      expect(div.getScrollTop()).toBe(0)

      // The window is replaced under a new generation (jumpToOldest's re-fetch, or a
      // trim), so the anchored top row no longer resolves. Nothing may move the view
      // off the NEW top row.
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
    }))

  it('stays pinned to the very top when leading collapsed rows measure taller (long-transcript Home)', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      // The top rows have a height of 0 (empty rows) and stack at offset 0; only
      // the rows below have a height.
      const { virt, setRowHeight, total } = makeRowVirtualizer([
        0,
        0,
        0,
        0,
        0,
        ...Array.from<number>({ length: 15 }).fill(100),
      ])
      div.setClientHeight(500)
      div.setScrollTop(0)
      createRenderEffect(() => div.setScrollHeight(total()))
      const [messages] = createSignal<AgentChatMessage[]>([])
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        hasNewerMessages: () => false,
        hasOlderMessages: () => false,
      }))
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
      // Clamped at the top: the page write can't move, so the browser emits no scroll
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
      // Room to move: the page write lands at 548 and (in a real browser) the native
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
      // Hidden/collapsed pane: clientHeight 0. The page delta would be 0, the page write
      // is a no-op, and scrollTop === before -- which WITHOUT the guard fires edge
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

describe('useChatScroll down-jump on a small scroll-down', () => {
  // Controllable virtualizer with a single anchored row at `rowOffset`; `prepend`
  // grows the content above it (and the total height) like older history landing.
  function makeAnchorVirt(rowOffset0: number, total0: number) {
    let total = total0
    let rowOffset = rowOffset0
    const [version, setVersion] = createSignal(0)
    const virt = withContentAnchors({
      ...virtualizerNoOps(),
      totalHeight: () => {
        version()
        return total
      },
      geometryVersion: version,
      updateViewport: () => {},
      anchorAt: scrollTop => ({ id: 'row', offsetWithinRow: scrollTop - rowOffset }),
      scrollTopNearAnchor: a => rowOffset + a.offsetWithinRow,
      scrollTopForAnchor: a => rowOffset + a.offsetWithinRow,
    })
    const prepend = (px: number) => {
      rowOffset += px
      total += px
      setVersion(v => v + 1)
    }
    return { virt, prepend }
  }

  it('stays anchored 20px above the tail', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      // Short window (the hidden-heavy case): only 100px of scroll range, so the
      // whole content sits within ~a viewport. Bottom is at scrollTop 100.
      div.setClientHeight(500)
      div.setScrollHeight(600)
      const ctrl = makeAnchorVirt(0, 600)
      const [messages] = createSignal<AgentChatMessage[]>([])
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: ctrl.virt,
        messages,
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Leave follow mode by scrolling to the top.
      div.setScrollTop(0)
      hook.handlers.onScroll()
      // Scroll DOWN a bit: distFromBottom = 600 - 80 - 500 = 20 -- more than the 2px
      // follow tolerance, so the reader stays where they are and does not follow.
      div.setScrollTop(80)
      hook.handlers.onScroll()
      expect(hook.isFollowing()).toBe(false)

      // 200px of older content lands above. The reader does not follow, so no stick
      // moves the view to the new bottom (300). This stub has no layout shift, so the
      // view stays at the same scroll position.
      div.setScrollHeight(800)
      ctrl.prepend(200)
      await Promise.resolve()
      await Promise.resolve()
      expect(div.getScrollTop()).toBe(80)
      expect(hook.isFollowing()).toBe(false)
    }))

  it('does not re-engage tail-follow (no down-chase) at a windowed-away loaded bottom', () =>
    inRoot(async () => {
      const div = makeFakeScrollDiv()
      div.setClientHeight(500)
      div.setScrollHeight(1000)
      div.setScrollTop(500) // at the bottom (the live tail, initially)

      // Growable total + a real anchor, so dropping follow yields ANCHORED (not
      // following). At the loaded-window bottom with newer content windowed away,
      // captureViewportAnchor pins the BOTTOM row (ratio 1), so the anchor sits at the
      // viewport-bottom line scrollTop + clientHeight (600 + 500 = 1100); returning that
      // as scrollTopForAnchor keeps any placement a no-op when newer appends BELOW the
      // anchor -- isolating the follow path.
      const [total, setTotal] = createSignal(1000)
      const virt = withContentAnchors({
        ...virtualizerNoOps(),
        totalHeight: () => total(),
        geometryVersion: () => 0,
        updateViewport: () => {},
        anchorAt: () => ({ id: 'row', offsetWithinRow: 0 }),
        scrollTopNearAnchor: () => 1100,
        scrollTopForAnchor: () => 1100,
      })
      const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
      const [hasNewer, setHasNewer] = createSignal(false)
      const [agentStatus, setAgentStatus] = createSignal<AgentStatus | undefined>(AgentStatus.ACTIVE)
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: virt,
        messages,
        agentStatus,
        hasNewerMessages: hasNewer,
      }))
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()

      // Stick to the bottom at the LIVE tail: a tracked-input change plus a grown
      // scrollHeight makes the auto-scroll effect stick to the new bottom.
      div.setScrollHeight(1100)
      setAgentStatus(AgentStatus.STARTING)
      await Promise.resolve()
      await Promise.resolve()
      expect(div.getScrollTop()).toBe(600) // stuck at the new bottom of 1100

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
    }))
})

describe('useChatScroll momentum reporting', () => {
  /**
   * `onMomentumScroll` is what keeps the floating scroll rail lit through a
   * flick: no touch or pointer event fires while the content glides, so the
   * rail can only relight from `scroll` events -- and only this hook can say
   * which of those are the reader's own coast rather than a stick-to-bottom
   * write echoing back. A rail lit by the latter would stay lit for a whole
   * streaming response.
   */
  function setupMomentum() {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(2000)
    div.setClientHeight(500)
    div.setScrollTop(0)
    const [messages] = createSignal<AgentChatMessage[]>([])
    let momentumReports = 0
    const hook = useChatScroll(withStaticAdmission({
      virtualizer: makeStubVirtualizer(),
      messages,
      onMomentumScroll: () => { momentumReports++ },
    }))
    hook.attachListRef(div.el)
    return {
      div,
      hook,
      reports: () => momentumReports,
    }
  }

  it('reports a scroll the reader coasted to', () =>
    createRoot((dispose) => {
      const { div, hook, reports } = setupMomentum()

      // Landed somewhere this hook never wrote, with no finger down: a coast.
      div.setScrollTop(300)
      hook.handlers.onScroll()

      expect(reports()).toBe(1)
      dispose()
    }))

  it('reports nothing while the finger is still down', () =>
    createRoot((dispose) => {
      const { div, hook, reports } = setupMomentum()

      // A drag under the finger has no inertia to protect, and the pointer
      // events it fires already light the rail on their own.
      hook.handlers.onTouchStart({
        touches: [
          { clientY: 10 },
        ],
      } as unknown as TouchEvent)
      div.setScrollTop(300)
      hook.handlers.onScroll()

      expect(reports()).toBe(0)

      // The finger lifts and the content glides on: the same scroll now counts,
      // which is what proves the silence above was the guard and not the setup.
      hook.handlers.onTouchEnd({ touches: [] } as unknown as TouchEvent)
      div.setScrollTop(600)
      hook.handlers.onScroll()

      expect(reports()).toBe(1)
      dispose()
    }))

  it('reports nothing for its own write echoing back', () =>
    inRoot(async () => {
      const { div, hook, reports } = setupMomentum()
      // The command runs outside the update of the root, as a key press does. Inside the
      // update, the hook shows the place only when the update completes.
      await Promise.resolve()

      // The stick-to-bottom's write, then the native scroll event it causes.
      hook.forceScrollToBottom()
      hook.handlers.onScroll()

      expect(reports()).toBe(0)

      // A reader-driven move away from that landing IS reported, so the silence
      // above was the own-write rule and not a harness that reports nothing at all.
      div.setScrollTop(div.getScrollTop() - 400)
      hook.handlers.onScroll()

      expect(reports()).toBe(1)
    }))

  it('does not count the position at the mount as a move', () =>
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(2000)
      div.setClientHeight(500)
      // A window paged away from the tail, so no mount placement moves the view.
      div.setScrollTop(1000)
      const [messages] = createSignal<AgentChatMessage[]>([])
      let momentumReports = 0
      const hook = useChatScroll(withStaticAdmission({
        virtualizer: makeStubVirtualizer(),
        messages,
        hasNewerMessages: () => true,
        onMomentumScroll: () => { momentumReports++ },
      }))
      hook.attachListRef(div.el)

      // The first event at the position of the mount: with a baseline of 0 it would
      // count as a move of 1000px.
      hook.handlers.onScroll()

      expect(momentumReports).toBe(0)
      dispose()
    }))
})

describe('useChatScroll one pending history jump', () => {
  // The hook owns one pending history jump: the latest command of the reader stops each
  // earlier fetch, and so does each input by which the reader takes control. Without that,
  // the page of Home arrives after a later command and replaces the window under it, and
  // the landing of Home moves the view to the top.

  /** Let a scroll-to-bottom animation run to its end. */
  async function finishAnimation(h: { frame: () => Promise<void> }): Promise<void> {
    for (let i = 0; i < 80; i++)
      await h.frame()
  }

  /**
   * A reader at 2000 in a window with older history, who pressed Home: the jump to the
   * oldest page stays pending until the test settles `jump`. `signal` is the signal that
   * Home passed to the store. Row `i` has the seq `i + 1`. With `holdTargets`, the target
   * of each request waits until the test reaches it.
   */
  async function homePressed(opts: { holdTargets?: boolean } = {}) {
    const jump = deferredJump()
    const h = mountScrollHarness({
      messages: rowMessages(1),
      ...opts,
      hookOptions: {
        hasOlderMessages: () => true,
        onJumpToOldest: jump.onJumpToOldest,
      },
    })
    await h.anchoredAt(2000)
    h.press('Home')
    expect(jump.onJumpToOldest).toHaveBeenCalledOnce()
    const signal = jump.onJumpToOldest.mock.calls[0]![0]!
    expect(signal.aborted).toBe(false)
    return { h, jump, signal }
  }

  it('lands at the top when the page of Home arrives', async () => {
    const { h, jump } = await homePressed()

    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(0)
    h.dispose()
  })

  it('saves the seq of the first row with a top that waits, so a later restore finds the nearest row', () => {
    const h = mountScrollHarness({
      messages: rowMessages(1),
      holdTargets: true,
    })

    // Home with no older history asks for the top of the loaded rows, and the top waits.
    h.press('Home')

    expect(h.requestedTargets()).toEqual([
      { kind: 'top' },
    ])
    expect(h.hook.getScrollState()).toEqual({
      anchor: { id: 'r0', offsetWithinRow: 0, seq: 1n },
      atBottom: false,
      hasMoreNewer: false,
    })
    h.dispose()
  })

  it('asks for no place of the old window when the page of Home replaces the rows before its landing', async () => {
    const { h, jump } = await homePressed({ holdTargets: true })
    const requestsBefore = h.requestedTargets().length

    // The store applies the page of Home: every loaded row is replaced, and none is in
    // the list yet. The landing of Home runs in a later microtask.
    h.setAwaitingFirstView(true)
    expect(h.requestedTargets()).toHaveLength(requestsBefore)

    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.requestedTargets().slice(requestsBefore)).toEqual([
      { kind: 'top' },
    ])
    h.dispose()
  })

  it('asks for the place of the reader when the fetch of Home fails after the rows were replaced', async () => {
    const { h, jump } = await homePressed({ holdTargets: true })
    const requestsBefore = h.requestedTargets().length
    h.setAwaitingFirstView(true)

    jump.reject(new Error('the fetch failed'))
    await settleJump(jump.promise)

    // The reader was at row 20, so the first view goes there.
    expect(h.requestedTargets().slice(requestsBefore)).toEqual([
      rowTargetAt('r20'),
    ])
    h.dispose()
  })

  it('stops the fetch of Home for the scroll-to-bottom button with no newer page', async () => {
    const { h, jump, signal } = await homePressed()

    h.hook.scrollToBottom()
    expect(signal.aborted).toBe(true)
    jump.resolve(true)
    await settleJump(jump.promise)
    await finishAnimation(h)

    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('stops the fetch of Home for a send', async () => {
    const { h, jump, signal } = await homePressed()

    h.hook.forceScrollToBottom()
    expect(signal.aborted).toBe(true)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('stops the fetch of Home for the End key', async () => {
    const { h, jump, signal } = await homePressed()

    h.press('End')
    expect(signal.aborted).toBe(true)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('stops the fetch of Home for a seek inside the window', async () => {
    const { h, jump, signal } = await homePressed()

    // Row 30 has seq 31, and its top is at 3000. The landing keeps 8px above it.
    const landing = h.hook.jumpToSeq(31n)
    expect(signal.aborted).toBe(true)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(await landing).toBe(true)
    expect(h.div.getScrollTop()).toBe(2992)
    h.dispose()
  })

  it('stops the fetch of Home when the reader wheels', async () => {
    const { h, jump, signal } = await homePressed()
    h.notePlacement.mockClear()

    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: 100 }))
    expect(signal.aborted).toBe(true)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.notePlacement).not.toHaveBeenCalled()
    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })

  it('keeps the fetch of Home through a momentum scroll with no input', async () => {
    const { h, jump, signal } = await homePressed()

    h.scrollTo(1900)
    expect(signal.aborted).toBe(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.getScrollTop()).toBe(0)
    h.dispose()
  })

  it('places nothing when another fetch replaced the page of Home', async () => {
    const { h, jump } = await homePressed()
    h.notePlacement.mockClear()
    // The browser moved the view to the bottom with no scroll event, so only a position
    // check can publish that the view is at the bottom.
    h.div.setScrollTop(4500)
    expect(h.hook.atBottom()).toBe(false)

    jump.resolve(false)
    await settleJump(jump.promise)

    // The window is not the oldest page, so its top is not the first message.
    expect(h.notePlacement).not.toHaveBeenCalled()
    expect(h.div.getScrollTop()).toBe(4500)
    // The landing checked the position instead.
    expect(h.hook.atBottom()).toBe(true)
    h.dispose()
  })

  it('stops the fetch of Home when the hook unmounts', async () => {
    const { h, jump, signal } = await homePressed()
    const writes = h.div.scrollTopWrites()

    h.dispose()
    expect(signal.aborted).toBe(true)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.div.scrollTopWrites()).toBe(writes)
    expect(h.div.getScrollTop()).toBe(2000)
  })

  it('ends the request for the tail when an input stops a jump to the latest page', async () => {
    const { h, jump } = await mountWithPendingJump(mountScrollHarness)
    const signal = jump.onJumpToLatest.mock.calls[0]![0]!
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: true })

    // A wheel with no scroll event: the list cannot move, but the reader took control.
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: 100 }))
    expect(signal.aborted).toBe(true)
    // The request has no fetch left. A save must not record it, or the next mount runs
    // the jump that the reader stopped.
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)

    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
    expect(h.div.getScrollTop()).toBe(2000)
    h.dispose()
  })
})

// The rows of the place that a command goes to can be staged: loaded, and not measured
// yet. The command then waits, and the view moves once, when the rows are reached.
describe('useChatScroll a command that waits for its rows', () => {
  // The harness: 50 rows of 100px in a 500px view, so the bottom is at 4500. The target of
  // each request waits until the test reaches it.
  it('keeps the view for Home until the first rows are reached', () => {
    const h = mountScrollHarness({ holdTargets: true })
    expect(h.div.getScrollTop()).toBe(4500)

    h.press('Home')
    expect(h.div.getScrollTop()).toBe(4500)
    expect(h.requestedTargets()).toEqual([
      { kind: 'top' },
    ])

    h.reachTarget()
    expect(h.div.getScrollTop()).toBe(0)
    h.dispose()
  })

  it('keeps the view for End with rows staged below, and follows the tail once they are reached', async () => {
    const h = mountScrollHarness({ holdTargets: true, stagedNewer: true })
    await h.anchoredAt(2000)

    h.press('End')
    // The list grows while the tail waits. A stick here would put the view at the bottom
    // of the measured rows, and the wait would move it again.
    h.changeBelow(300)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(2000)

    h.reachTarget()
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('ends follow mode for Home at the bottom of the list, and saves the top as the place of the reader', () => {
    const onSaveViewportScroll = vi.fn()
    const h = mountScrollHarness({
      holdTargets: true,
      hookOptions: { onSaveViewportScroll },
    })

    h.press('Home')
    h.changeBelow(300)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.div.getScrollTop()).toBe(4500)

    // An unmount during the wait keeps the place that the reader asked for.
    h.dispose()
    expect(onSaveViewportScroll).toHaveBeenCalledOnce()
    const saved = onSaveViewportScroll.mock.calls[0]![0]
    expect(saved).toMatchObject({
      anchor: { id: 'r0', offsetWithinRow: 0 },
      atBottom: false,
    })
    expect(saved).not.toHaveProperty('rawScrollTop')
  })

  it('ends a wait for the tail for an input of the reader', async () => {
    const h = mountScrollHarness({ holdTargets: true, stagedNewer: true })
    await h.anchoredAt(2000)
    h.press('End')

    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: 100 }))
    h.scrollTo(2100)
    h.changeBelow(300)
    h.reachTarget()

    expect(h.div.getScrollTop()).toBe(2100)
    expect(h.hook.isFollowing()).toBe(false)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: false })
    h.dispose()
  })

  it('changes nothing for an input while loaded rows exist and none is in the list', () => {
    const h = mountScrollHarness({ holdTargets: true, awaitingFirstView: true })
    // The first-view rule asked for the tail.
    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])

    // The reader cannot take control of a view that shows no row.
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -100 }))
    h.hook.pageScroll(1)
    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])

    h.reachTarget()
    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('keeps the request for the tail for an upward input while loaded rows exist and none is in the list', () => {
    const h = mountScrollHarness({ holdTargets: true, awaitingFirstView: true, scrollTopBeforeMount: 2000 })
    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])

    // The view has room to move up, but no row shows a position. So neither input is a
    // move of the reader away from the bottom.
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -100 }))
    h.hook.pageScroll(-1)

    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: true })
    h.dispose()
  })

  /** A mount on a window with newer pages whose saved state asks for the tail: the mount starts the jump to the latest page, which stays pending. */
  function mountedWithPendingLatestJump() {
    const jump = deferredJump()
    const h = mountScrollHarness({
      holdTargets: true,
      hasNewer: true,
      awaitingFirstView: true,
      hookOptions: {
        savedViewportScroll: () => ({ atBottom: true, hasMoreNewer: true }),
        onClearSavedViewportScroll: vi.fn(),
        onJumpToLatest: jump.onJumpToLatest,
      },
    })
    expect(jump.onJumpToLatest).toHaveBeenCalledOnce()
    const signal = jump.onJumpToLatest.mock.calls[0]![0]!
    return { h, jump, signal }
  }

  it('keeps a jump to the latest page through each input during the first view', async () => {
    const { h, jump, signal } = mountedWithPendingLatestJump()

    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: 100 }))
    h.hook.pageScroll(1)
    h.hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -100 }))

    // No input aborts the jump, and none ends its request for the tail.
    expect(signal.aborted).toBe(false)
    expect(h.hook.getScrollState()).toMatchObject({ atBottom: true })

    h.setHasNewer(false)
    jump.resolve(true)
    await settleJump(jump.promise)
    h.reachTarget()

    expect(h.div.getScrollTop()).toBe(h.maxScrollTop())
    expect(h.hook.isFollowing()).toBe(true)
    h.dispose()
  })

  it('asks for the tail once for End during the first view of a pending jump', () => {
    const { h, signal } = mountedWithPendingLatestJump()
    // The mount asks for no place while its jump is pending.
    expect(h.requestedTargets()).toEqual([])
    h.setHasNewer(false)

    h.press('End')

    // The command ends the jump before it, and its own request is the only one: the
    // first-view rule sees one update, and asks for nothing between its steps.
    expect(signal.aborted).toBe(true)
    expect(h.requestedTargets()).toEqual([
      { kind: 'tail' },
    ])
    h.dispose()
  })
})
