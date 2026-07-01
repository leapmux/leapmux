import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRenderEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'

import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeRowVirtualizer, makeStubVirtualizer } from './useChatScroll.testkit'

installScrollTestEnv()

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
