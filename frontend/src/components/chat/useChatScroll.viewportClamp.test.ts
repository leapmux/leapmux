import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { triggerResizeObservers } from '~/test-support/resizeObserverStub'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeRowVirtualizer } from './useChatScroll.testkit'

installScrollTestEnv()

function resizeAndAppend(options: {
  clientHeight: number
  contentHeight: number
  gesture?: 'wheel' | 'keyboard' | 'pointer' | 'page' | 'coast'
  browsing?: boolean
  displacement?: number
  probeEcho?: boolean
  growAfterClamp?: boolean
  growBeforeResize?: boolean
}) {
  return createRoot(async (dispose) => {
    let now = 1000
    const clock = options.gesture === 'coast' || options.growAfterClamp ? vi.spyOn(performance, 'now').mockImplementation(() => now) : undefined
    try {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(1000)
      div.setClientHeight(200)
      div.setScrollTop(800)
      const rows = makeRowVirtualizer(Array.from<number>({ length: 10 }).fill(100))
      const [messages] = createSignal<AgentChatMessage[]>([])
      const onMomentumScroll = vi.fn()
      const hook = useChatScroll({ virtualizer: rows.virt, messages, onMomentumScroll })
      hook.attachListRef(div.el)
      await Promise.resolve()
      await Promise.resolve()
      hook.jumpToBottom()
      hook.handlers.onScroll()
      expect(hook.atBottom()).toBe(true)
      if (options.browsing) {
        div.setScrollTop(200)
        hook.handlers.onScroll()
        expect(hook.atBottom()).toBe(false)
      }
      if (options.gesture === 'coast') {
        hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -400 }))
        now += 16
        div.setScrollTop(400)
        hook.handlers.onScroll()
        now += 16
        div.setScrollTop(800)
        hook.handlers.onScroll()
        // Keep moving slowly after the explicit input grace ends.
        for (let tick = 0; tick < 60; tick++) {
          now += 16
          div.setScrollTop(tick % 2 === 0 ? 799 : 800)
          hook.handlers.onScroll()
        }
        expect(hook.atBottom()).toBe(true)
      }
      if (options.gesture === 'page')
        hook.pageScroll(-1)
      // Layout changes before the browser delivers the old position's scroll event.
      if (options.growBeforeResize) {
        div.setScrollHeight(options.contentHeight)
        div.setClientHeight(options.clientHeight)
      }
      else {
        div.setClientHeight(options.clientHeight)
        div.setScrollHeight(options.contentHeight)
      }
      if (options.gesture === 'wheel')
        hook.handlers.onWheel(new WheelEvent('wheel', { deltaY: -1 }))
      if (options.gesture === 'keyboard')
        hook.handlers.onKeyDown(new KeyboardEvent('keydown', { key: 'ArrowUp' }))
      if (options.gesture === 'pointer')
        hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
      if (options.displacement)
        div.setScrollTop(div.getScrollTop() + options.displacement)
      const beforeEvent = div.getScrollTop()
      onMomentumScroll.mockClear()
      now += 16
      hook.handlers.onScroll()
      const layoutMomentumEvents = onMomentumScroll.mock.calls.length
      rows.setRowHeight(9, options.contentHeight - 900)
      await triggerResizeObservers()
      if (options.growAfterClamp) {
        now += 16
        div.setScrollHeight(5000)
        hook.handlers.onScroll()
        rows.setRowHeight(9, 4100)
        await triggerResizeObservers()
      }
      onMomentumScroll.mockClear()
      if (options.probeEcho)
        hook.handlers.onScroll()
      return { beforeEvent, layoutMomentumEvents, scrollTop: div.getScrollTop(), atBottom: hook.atBottom(), momentumEvents: onMomentumScroll.mock.calls.length }
    }
    finally {
      dispose()
      clock?.mockRestore()
    }
  })
}

describe('useChatScroll viewport clamp', () => {
  it('keeps following when content grows before the viewport expands', async () => {
    const result = await resizeAndAppend({ clientHeight: 250, contentHeight: 5000, growBeforeResize: true })
    expect(result.beforeEvent).toBe(800)
    expect(result.scrollTop).toBe(4750)
    expect(result.atBottom).toBe(true)
    expect(result.layoutMomentumEvents).toBe(0)
  })

  it.each([
    { clientHeight: 500, contentHeight: 5000 },
    { clientHeight: 100, contentHeight: 5000 },
    { clientHeight: 200, contentHeight: 5000 },
    { clientHeight: 500, contentHeight: 950 },
  ])('keeps following across layout changes before the scroll event: %j', async (geometry) => {
    const result = await resizeAndAppend(geometry)
    expect(result.scrollTop).toBe(geometry.contentHeight - geometry.clientHeight)
    expect(result.atBottom).toBe(true)
  })

  it.each(['wheel', 'keyboard', 'pointer'] as const)('keeps a reader in control during simultaneous %s input', async (gesture) => {
    const result = await resizeAndAppend({ clientHeight: 500, contentHeight: 5000, gesture })
    expect(result.scrollTop).toBe(result.beforeEvent)
    expect(result.atBottom).toBe(false)
  })

  it('recognizes an already-clamped layout event without inventing momentum', async () => {
    const result = await resizeAndAppend({ clientHeight: 500, contentHeight: 950 })
    expect(result.scrollTop).toBe(450)
    expect(result.layoutMomentumEvents).toBe(0)
  })

  it('keeps following the next growth after a layout event needs no additional scroll write', async () => {
    const result = await resizeAndAppend({ clientHeight: 500, contentHeight: 950, growAfterClamp: true })
    expect(result.scrollTop).toBe(4500)
    expect(result.atBottom).toBe(true)
  })

  it('retains the correction marker until its own browser event arrives', async () => {
    const result = await resizeAndAppend({ clientHeight: 500, contentHeight: 5000, probeEcho: true })
    expect(result.scrollTop).toBe(4500)
    expect(result.momentumEvents).toBe(0)
  })

  it('preserves the slow end of a trackpad coast after the input grace expires', async () => {
    const result = await resizeAndAppend({ clientHeight: 500, contentHeight: 5000, gesture: 'coast' })
    expect(result.scrollTop).toBe(result.beforeEvent)
    expect(result.atBottom).toBe(false)
  })

  it('keeps a pending page scroll in control when it matches the layout clamp', async () => {
    const result = await resizeAndAppend({ clientHeight: 352, contentHeight: 5000, gesture: 'page' })
    expect(result.beforeEvent).toBe(648)
    expect(result.scrollTop).toBe(result.beforeEvent)
    expect(result.atBottom).toBe(false)
  })

  it('preserves a position that differs from the known layout clamp', async () => {
    const result = await resizeAndAppend({ clientHeight: 500, contentHeight: 5000, displacement: -100 })
    expect(result.scrollTop).toBe(result.beforeEvent)
    expect(result.atBottom).toBe(false)
  })

  it('keeps a reader above the tail when the resize clamps the reading position', async () => {
    const result = await resizeAndAppend({ clientHeight: 800, contentHeight: 5000, browsing: true })
    expect(result.scrollTop).toBe(result.beforeEvent)
    expect(result.atBottom).toBe(false)
  })
})
