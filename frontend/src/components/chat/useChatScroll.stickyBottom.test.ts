import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'

import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeStubVirtualizer } from './useChatScroll.testkit'

installScrollTestEnv()

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
          const [agentWorking] = createSignal<boolean | undefined>(false)
          const [agentStatus, setAgentStatus] = createSignal<AgentStatus | undefined>(AgentStatus.ACTIVE)

          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
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
          // even though messages.length and messageVersion did not.
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

  it('still saves the at-bottom state even without an anchor', () => {
    createRoot((dispose) => {
      const div = makeFakeScrollDiv()
      div.setScrollHeight(500)
      div.setClientHeight(500) // content fits -> at the bottom
      const [messages] = createSignal<AgentChatMessage[]>([])
      const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages })
      hook.attachListRef(div.el)
      hook.handlers.onScroll()
      const state = hook.getScrollState()
      expect(state).toBeDefined()
      expect(state!.atBottom).toBe(true)
      expect(state!.anchor).toBeUndefined()
      dispose()
    })
  })
})

describe('usechatscroll scroll-to-bottom animation', () => {
  it('hands off to sticky-bottom instead of chasing a target that grows every frame', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          let scrollHeight = 10000
          div.setScrollHeight(scrollHeight)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          hook.scrollToBottomAnimated()
          // Grow the target far faster than the step can close it each frame, so the
          // animation can only terminate via the frame cap -- never by reaching the
          // bottom. stickToBottom jumps scrollTop to the absolute bottom; the chase
          // (scrollTop += step) never lands there while the target keeps moving.
          let frames = 0
          let stuck = false
          while (frames < 200) {
            scrollHeight += 100000
            div.setScrollHeight(scrollHeight)
            await Promise.resolve() // run one animate frame against this height
            frames++
            if (div.getScrollTop() === scrollHeight - 500) {
              stuck = true
              break
            }
          }
          expect(stuck).toBe(true)
          // The frame cap is 60; the hand-off fires shortly after. An uncapped loop
          // would chase all the way to the 200-frame safety bound.
          expect(frames).toBeLessThanOrEqual(65)
          expect(hook.isAtBottomFresh()).toBe(true)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('a tap (pointerdown) mid-animation stops the coasting scroll immediately', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(10000)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const hook = useChatScroll({ virtualizer: makeStubVirtualizer(), messages })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Park at the top AFTER the mount restick settles, so the animation genuinely
          // has 9500px to travel.
          div.setScrollTop(0)
          hook.handlers.onScroll()
          expect(div.getScrollTop()).toBe(0)

          // Start the animation and grab the surface BEFORE its first frame delivers
          // (the testkit rAF fires on a microtask, so nothing has run yet). The grab's
          // cancelPendingScroll must cancel that queued frame so the view never moves.
          // (The testkit's rAF is cancelable for exactly this: with a no-op
          // cancelAnimationFrame the "cancelled" frame still fired, kept writing
          // scrollTop, and this regression was unobservable.)
          hook.scrollToBottomAnimated()
          hook.handlers.onPointerDown(new PointerEvent('pointerdown', { pointerId: 1, isPrimary: true }))
          await Promise.resolve()
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(0) // the queued frame never ran
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
