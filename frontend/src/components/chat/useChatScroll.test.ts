import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { beforeAll, describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import {
  installControllableResizeObserver,
  triggerResizeObserverForSync,
  triggerResizeObserversSync,
} from '../../../tests/unit/helpers/resizeObserverStub'
import { useChatScroll } from './useChatScroll'

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

interface FakeScrollDiv {
  el: HTMLDivElement
  setScrollHeight: (n: number) => void
  setClientHeight: (n: number) => void
  setClientWidth: (n: number) => void
  setScrollTop: (n: number) => void
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

  it('refreshes sticky records when the user scrolls back within the bottom threshold', () =>
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
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(300)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // Returning to within the sticky threshold should normalize to the
          // exact clamped bottom and refresh the sticky record.
          div.setScrollTop(480)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(500)
          expect(hook.atBottom()).toBe(true)

          div.setScrollHeight(1500)
          hook.handlers.onScroll()

          expect(div.getScrollTop()).toBe(1000)
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

  it('sticks to bottom on content-only resize from the observed content element', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          const content = document.createElement('div')
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setClientWidth(800)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          hook.attachContentRef(content)

          await Promise.resolve()
          await Promise.resolve()

          // Only content height changes; the scroll container dimensions stay
          // fixed. This must still preserve sticky-bottom state.
          div.setScrollHeight(1100)
          triggerResizeObserverForSync(content)
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

  it('observes a content ref attached after mount', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          const content = document.createElement('div')
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)

          await Promise.resolve()
          await Promise.resolve()

          hook.attachContentRef(content)
          div.setScrollHeight(1100)
          triggerResizeObserverForSync(content)
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
          const content = document.createElement('div')
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500)

          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')

          const hook = useChatScroll({
            messages,
            streamingText,
          })
          hook.attachListRef(div.el)
          hook.attachContentRef(content)

          await Promise.resolve()
          await Promise.resolve()

          hook.forceScrollToBottom()
          div.setScrollHeight(1100)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(600)
          expect(hook.atBottom()).toBe(true)

          div.setScrollHeight(1250)
          triggerResizeObserverForSync(content)
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
})
