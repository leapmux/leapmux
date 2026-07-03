import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'

import { FLING_SETTLE_MS } from './chatScrollFlingSettle'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, measurementDeferralNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

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
            ...measurementDeferralNoOps(),
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
      ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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

  it('flags fast-scroll for the skeletons on a direct DRAG and clears it after the settle window', () =>
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
          const setFastScrollActive = vi.fn()
          const setVisibleMeasurementDeferral = vi.fn()
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 8000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: anchor => anchor.offsetWithinRow,
            setFastScrollActive,
            setVisibleMeasurementDeferral,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // A scrollbar-thumb drag: pointer down, then a fast scroll event with
          // NO wheel/momentum input — the momentum-only deferral must stay off,
          // but rows entering during the drag pay the same mount cost as during
          // a fling, so the skeleton gate must still engage.
          hook.handlers.onPointerDown({ isPrimary: true, pointerId: 7 } as PointerEvent)
          div.setScrollTop(900)
          hook.handlers.onScroll()
          await Promise.resolve()

          expect(setFastScrollActive).toHaveBeenLastCalledWith(true)
          expect(setVisibleMeasurementDeferral).not.toHaveBeenCalledWith(true)

          // A drag has no fling-settle to clear the flag; the trailing debounce
          // (same window as the velocity tracker's idle) drops it.
          vi.advanceTimersByTime(FLING_SETTLE_MS)
          await Promise.resolve()
          expect(setFastScrollActive).toHaveBeenLastCalledWith(false)

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

  it('does NOT flag fast-scroll for an idle non-echo scroll event with no user input', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const setFastScrollActive = vi.fn()
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 8000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: anchor => anchor.offsetWithinRow,
            setFastScrollActive,
            setVisibleMeasurementDeferral: () => {},
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // A NON-echo scroll event with no pointer/touch drag and no recent wheel/touch
          // momentum: a stick echo delivered past its marker TTL, browser scroll
          // anchoring, or a sub-pixel nudge while the reader sits idle at the tail. Its
          // velocity is unknown/stale, so isFling() reports true -- but it is NOT a user
          // fast-scroll, so the fling-skeleton flag must stay off (otherwise a freshly
          // appended, still-unmeasured tail row flashes a one-frame skeleton).
          div.setScrollTop(900)
          hook.handlers.onScroll()
          await Promise.resolve()

          expect(setFastScrollActive).not.toHaveBeenCalledWith(true)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('flags fast-scroll for a momentum (wheel) fling via recent momentum input', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(40000)
          div.setScrollTop(300)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const setFastScrollActive = vi.fn()
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 8000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: scrollTop => ({ id: 'anchored-row', offsetWithinRow: scrollTop }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: anchor => anchor.offsetWithinRow,
            setFastScrollActive,
            setVisibleMeasurementDeferral: () => {},
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // A wheel gesture marks momentum input; the fast scroll event it drives then
          // enters through the SAME idle-looking path (no active pointer) but carries a
          // live user-scroll signal (hasRecentMomentumInput), so the skeleton engages.
          hook.handlers.onWheel({ deltaY: 400, deltaX: 0, ctrlKey: false } as WheelEvent)
          div.setScrollTop(900)
          hook.handlers.onScroll()
          await Promise.resolve()

          expect(setFastScrollActive).toHaveBeenCalledWith(true)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
            ...measurementDeferralNoOps(),
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
