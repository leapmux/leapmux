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

  it('releases the hold once the toggled row re-measures (resize settled), so a scrollbar drag is not frozen', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // A scrollbar-thumb drag fires only `scroll` events -- no wheel/key/touch/pointer to
          // release the toggle hold. If the hold lingered past the resize, a later geometry
          // change would yank the view back to the toggled row. So the hold releases on RESIZE
          // COMPLETION: once the toggled row's real height commits again (hasMeasuredHeight
          // true after the estimate phase), normal midpoint anchoring resumes.
          const div = makeFakeScrollDiv()
          const base = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          // All rows start measured; a toggle takes the row unmeasured (estimate phase) then
          // measured again. hasMeasuredHeight drives the completion-based release.
          const measured = new Set(Array.from({ length: 20 }, (_, i) => `g0_${i}`))
          const virt = { ...base.virt, hasMeasuredHeight: (id: string) => measured.has(id) }
          const { setRowHeight, total } = base
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

          div.setScrollTop(800) // viewport [800,1300]; row 8's top (offset 800) sits at the viewport top
          hook.handlers.onScroll()
          hook.anchorRowForResize('g0_8') // pin row 8's top (ratio 0), arm the hold

          // Estimate phase: the toggle takes row 8 unmeasured. The geometry change fires the
          // re-pin, but the hold must STAY armed (hasMeasuredHeight false) -- releasing here
          // would drop the pin mid-resize.
          measured.delete('g0_8')
          setRowHeight(8, 100) // geomVersion bump; still the estimate slot (height unchanged)
          await Promise.resolve()
          await Promise.resolve()

          // Measured phase: row 8's real (expanded) height commits. The re-pin holds row 8's
          // top at offset 800 (scrollTop stays 800), and the hold now RELEASES.
          measured.add('g0_8')
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(800) // toggled row stayed stationary through the resize

          // A scrollbar-thumb drag: only a `scroll` event, no gesture. With the hold released,
          // handleScroll re-captures the live midpoint anchor (row 8, within 350 at ratio 0.5).
          div.setScrollTop(900)
          hook.handlers.onScroll()

          // A large grow above the new midpoint. Released -> the RE-CAPTURED midpoint anchor
          // holds (scrollTop 900 -> 1300). Had the hold lingered, the stale row-8-top pin would
          // have yanked scrollTop to 1200 instead.
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

  it('re-pins the held row at its NEW line when a scroll-only drag moves the viewport mid-hold', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
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
          hook.anchorRowForResize('g0_8') // hold armed: row 8's top (800) at the viewport top

          // A scrollbar-thumb drag fires ONLY `scroll` events (no wheel/pointer input in
          // Firefox), so nothing releases the hold before this event. The hold must
          // re-pin the SAME row at its NEW viewport line (row 8's top now 200px below
          // the viewport top) instead of freezing the toggle-time capture.
          div.setScrollTop(600)
          hook.handlers.onScroll()

          // The toggled row's resize lands. Row 8's own growth doesn't move its top
          // (800), so holding it at the re-captured line keeps scrollTop at 600 -- the
          // stale toggle-time pin would have yanked the drag back up to 800.
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()

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

  it('releases the hold when a scroll-only move takes the held row off-screen (falls back to midpoint capture)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
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
          hook.anchorRowForResize('g0_8')

          // The drag moves a full viewport down: row 8's top (800) is now ABOVE the
          // viewport [1400,1900], so a row-top pin is unrepresentable -- the hold must
          // release and normal midpoint anchoring resume (row 16 at offset 1600).
          div.setScrollTop(1400)
          hook.handlers.onScroll()

          // Row 8 (now far above the midpoint anchor) grows +400: the keep-position
          // re-pin absorbs the growth above the anchor by scrolling down 400 -- midpoint
          // behavior, NOT a yank back to the stale row-8 pin (which would land at 800).
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()

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

  it('releases the hold on the public pageScroll API (shell hotkey), matching the container onKeyDown', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
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
          hook.anchorRowForResize('g0_8')

          // TileRenderer's focus hotkey calls the API's pageScroll directly (not the
          // container's onKeyDown wrapper). It must release the hold the same way, so
          // the page's own scroll event re-captures normally instead of leaving the
          // stale row-top pin for the next geometry commit to yank a full page back.
          hook.pageScroll(1) // scrollBy +452 (viewport minus overlap) -> 1252
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(1252)

          // Row 8 grows +400 ABOVE the (midpoint-anchored) viewport: keep-position
          // scrolls down by the growth. A surviving row-top hold would have written
          // scrollTop back toward 800 instead.
          setRowHeight(8, 500)
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(1652)
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

  it('keeps auto-scrolling at the live tail when a row is toggled there (does not freeze on the anchor)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // Regression: toggling a row while FOLLOWING the live tail must NOT switch to
          // 'anchored' mode. If it did, sticky-bottom (which requires isFollowing()) would
          // stop re-sticking, so new streamed/appended content would stop auto-scrolling and
          // the view would fall off the tail until the user next scrolled.
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
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

          // The hook auto-sticks to the bottom on mount: pinned to the live tail (following).
          // 2000px of content in a 500px pane -> maxScrollTop 1500.
          div.setScrollTop(1500)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(true)

          // The user expands the last row while sitting at the tail. Because the view is
          // following, anchorRowForResize is a no-op (it does not capture a row anchor).
          hook.anchorRowForResize('g0_19')
          // The expansion grows row 19 by 200px (content 2000 -> 2200, maxScrollTop 1700).
          setRowHeight(19, 300)
          await Promise.resolve()
          await Promise.resolve()

          // Sticky-bottom re-stuck to the grown bottom. Had the toggle captured a row anchor
          // (mode 'anchored'), the re-pin would have frozen scrollTop at 1500 -- off the tail.
          expect(div.getScrollTop()).toBe(1700)
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

describe('usechatscroll edge-aware bottom anchoring', () => {
  it('keeps the loaded-window bottom stationary when a below-midpoint row grows (windowed away from the tail)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // Mirror of the top-edge fix: parked at the loaded-window bottom while NEWER content
          // is windowed away (hasNewerMessages true), captureViewportAnchor pins the BOTTOM row
          // (ratio 1). A below-midpoint row measuring taller then keeps the bottom content in
          // view (scrollTop follows it) instead of sliding it off-screen below a midpoint pin.
          const div = makeFakeScrollDiv()
          const { virt, setRowHeight, total } = makeRowVirtualizer(Array.from<number>({ length: 20 }).fill(100))
          div.setClientHeight(500)
          div.setScrollTop(1500) // loaded bottom: 2000px content, maxScrollTop 1500
          createRenderEffect(() => div.setScrollHeight(total()))
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => true, // windowed away from the live tail
            hasOlderMessages: () => false,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park at the loaded bottom; captureAnchor pins the bottom row (ratio 1), NOT tail-
          // follow (hasNewerMessages is true, so atBottomForFollow is false). The capture does
          // not move scrollTop.
          div.setScrollTop(1500)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(1500)

          // Row 18 (top 1800, below the midpoint 1750) measures 200px taller.
          setRowHeight(18, 300)
          await Promise.resolve()
          await Promise.resolve()

          // The bottom row stays pinned to the viewport bottom, so scrollTop follows to the new
          // bottom (2200 - 500 = 1700). A midpoint pin (the pre-fix behavior) would have left
          // scrollTop at 1500 and slid the grown bottom rows off-screen below the viewport.
          expect(div.getScrollTop()).toBe(1700)
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
