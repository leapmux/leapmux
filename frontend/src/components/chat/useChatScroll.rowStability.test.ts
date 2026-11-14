import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'

import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeStubVirtualizer } from './useChatScroll.testkit'

installScrollTestEnv()

describe('useChatScroll explicit edge-intent tolerance', () => {
  function setupEdge(scrollTop: number) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(scrollTop)
    const [messages] = createSignal<AgentChatMessage[]>([])
    let olderLoads = 0
    let newerLoads = 0
    const hook = useChatScroll({
      virtualizer: makeStubVirtualizer(),
      messages,
      hasOlderMessages: () => true,
      hasNewerMessages: () => true,
      onLoadOlderMessages: () => { olderLoads++ },
      onLoadNewerMessages: () => { newerLoads++ },
    })
    hook.attachListRef(div.el)
    return {
      hook,
      getOlder: () => olderLoads,
      getNewer: () => newerLoads,
    }
  }

  it('loads older on a wheel-up at a sub-pixel scrollTop (fractional DPI/zoom)', () =>
    createRoot((dispose) => {
      // A layout change or clamp on a fractional-DPI display can leave scrollTop at 0.5 even
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

describe('useChatScroll overscroll-at-top drag', () => {
  function setupHook(scrollTop: number) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(scrollTop)
    const [messages] = createSignal<AgentChatMessage[]>([])
    let olderLoads = 0
    const hook = useChatScroll({
      virtualizer: makeStubVirtualizer(),
      messages,
      hasOlderMessages: () => true,
      onLoadOlderMessages: () => { olderLoads++ },
    })
    hook.attachListRef(div.el)
    return {
      hook,
      olderLoads: () => olderLoads,
    }
  }

  const touch = (clientY: number) => ({
    touches: [
      { clientY },
    ],
  }) as unknown as TouchEvent
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
