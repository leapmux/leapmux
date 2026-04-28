import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { beforeAll, describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { useChatScroll } from './useChatScroll'

beforeAll(() => {
  // jsdom does not provide ResizeObserver; the hook only uses it for
  // tab-switch viewport restoration, which the auto-scroll tests don't
  // exercise. A no-op stub is sufficient.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

interface FakeScrollDiv {
  el: HTMLDivElement
  setScrollHeight: (n: number) => void
  setClientHeight: (n: number) => void
  setScrollTop: (n: number) => void
  getScrollTop: () => number
}

/**
 * Build a real <div> with stubbed scroll/layout properties so the hook can
 * read scrollHeight / clientHeight / scrollTop and we can observe writes to
 * scrollTop. jsdom doesn't compute layout, so these have to be patched.
 */
function makeFakeScrollDiv(): FakeScrollDiv {
  const el = document.createElement('div')
  let scrollHeight = 0
  let clientHeight = 0
  let scrollTop = 0
  Object.defineProperty(el, 'scrollHeight', {
    get: () => scrollHeight,
    configurable: true,
  })
  Object.defineProperty(el, 'clientHeight', {
    get: () => clientHeight,
    configurable: true,
  })
  Object.defineProperty(el, 'scrollTop', {
    get: () => scrollTop,
    set: (v: number) => {
      scrollTop = v
    },
    configurable: true,
  })
  return {
    el,
    setScrollHeight: (n) => {
      scrollHeight = n
    },
    setClientHeight: (n) => {
      clientHeight = n
    },
    setScrollTop: (n) => {
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

          expect(div.getScrollTop()).toBe(1100)
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
