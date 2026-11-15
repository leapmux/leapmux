import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'

import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeStubVirtualizer, virtualizerNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

describe('useChatScroll stall indicators', () => {
  // Content 5000 / viewport 500 => max scrollTop 4500: scrollTop 0 is the top edge,
  // 4500 the bottom edge (distFromBottom 0). Fetch flags are signals so a test can
  // flip a fetch mid-flight; load handlers are no-ops (the in-flight guard already
  // suppresses pagination while a fetch flag is set).
  function setupStall(opts: {
    scrollTop: number
    fetchingOlder?: boolean
    fetchingNewer?: boolean
    hasOlder?: boolean
    hasNewer?: boolean
  }) {
    const div = makeFakeScrollDiv()
    div.setScrollHeight(5000)
    div.setClientHeight(500)
    div.setScrollTop(opts.scrollTop)
    const [messages] = createSignal<AgentChatMessage[]>([])
    const [fetchingOlder, setFetchingOlder] = createSignal(opts.fetchingOlder ?? false)
    const [fetchingNewer, setFetchingNewer] = createSignal(opts.fetchingNewer ?? false)
    const hook = useChatScroll({
      virtualizer: makeStubVirtualizer(),
      messages,
      hasOlderMessages: () => opts.hasOlder ?? true,
      hasNewerMessages: () => opts.hasNewer ?? true,
      fetchingOlder,
      fetchingNewer,
      onLoadOlderMessages: () => {},
      onLoadNewerMessages: () => {},
    })
    hook.attachListRef(div.el)
    return {
      hook,
      div,
      setFetchingOlder,
      setFetchingNewer,
    }
  }

  it('reports stalledOlder as TRUE when clamped at the top edge with an older fetch in flight', () =>
    createRoot((dispose) => {
      const { hook } = setupStall({ scrollTop: 0, fetchingOlder: true, hasOlder: true })
      expect(hook.stalledOlder()).toBe(true)
      dispose()
    }))

  it('keeps stalledOlder dark for a background older pre-fetch away from the top edge', () =>
    createRoot((dispose) => {
      // scrollTop well inside the buffer: a pre-fetch is in flight but the reader is
      // not waiting at the edge, so no indicator.
      const { hook } = setupStall({ scrollTop: 1000, fetchingOlder: true, hasOlder: true })
      expect(hook.stalledOlder()).toBe(false)
      dispose()
    }))

  it('keeps stalledOlder dark with no older fetch in flight, and with no older history', () =>
    createRoot((dispose) => {
      expect(setupStall({ scrollTop: 0, fetchingOlder: false, hasOlder: true }).hook.stalledOlder()).toBe(false)
      expect(setupStall({ scrollTop: 0, fetchingOlder: true, hasOlder: false }).hook.stalledOlder()).toBe(false)
      dispose()
    }))

  it('reports stalledNewer as TRUE when clamped at the bottom edge with a newer fetch in flight', () =>
    createRoot((dispose) => {
      const { hook } = setupStall({ scrollTop: 4500, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(true)
      dispose()
    }))

  it('keeps stalledNewer dark for a background newer pre-fetch away from the bottom edge', () =>
    createRoot((dispose) => {
      const { hook } = setupStall({ scrollTop: 3000, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))

  it('keys stalledNewer off distFromBottom, not scrollTop: a newer fetch at the TOP edge is dark', () =>
    createRoot((dispose) => {
      // At the top edge (scrollTop 0) distFromBottom is 4500 -- nowhere near the
      // bottom -- so a newer fetch here is a background pre-fetch, not a stall.
      const { hook } = setupStall({ scrollTop: 0, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))

  it('turns stalledNewer on when a position-only scroll clamps the view at the bottom edge', () =>
    createRoot((dispose) => {
      // Fetch already in flight while the reader is still inside the buffer; the
      // scroll INTO the edge is a position-only move (no fetch-flag change), so the
      // memo must re-measure off scrollTick.
      const { hook, div } = setupStall({ scrollTop: 3000, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(false)
      div.setScrollTop(4500)
      hook.handlers.onScroll()
      expect(hook.stalledNewer()).toBe(true)
      dispose()
    }))

  it('clears stalledNewer when the fetch ends, even without a scroll event', () =>
    createRoot((dispose) => {
      // A newer page that lands while anchored away from the tail grows content
      // BELOW the viewport without moving scrollTop (no scroll event). The fetch
      // flag flipping false must hide the indicator on its own.
      const { hook, setFetchingNewer } = setupStall({ scrollTop: 4500, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(true)
      setFetchingNewer(false)
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))

  it('clears stalledNewer when the list element detaches', () =>
    createRoot((dispose) => {
      // The stall memos read messageListRef (a plain ref they can't track); attach AND
      // detach bump the geometry tick so they re-evaluate. On unmount the indicator must
      // go dark rather than latch at its last value (no element = nothing to stall on).
      const { hook } = setupStall({ scrollTop: 4500, fetchingNewer: true, hasNewer: true })
      expect(hook.stalledNewer()).toBe(true)
      hook.attachListRef(undefined)
      expect(hook.stalledNewer()).toBe(false)
      dispose()
    }))
})

describe('useChatScroll slow scroll phase', () => {
  it('warns "slow scroll phase" when a refreshViewport render cascade blows the frame budget', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        // A controllable clock so a stubbed updateViewport can "block the main thread" for a
        // deterministic duration (monotonicNow reads performance.now).
        let clock = 1000
        const nowSpy = vi.spyOn(performance, 'now').mockImplementation(() => clock)
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          // The scroll-triggered render cascade stalls the main thread 60ms -- over the 50ms
          // budget -- simulated by advancing the clock inside updateViewport (the synchronous
          // mount of entering rows + the premeasure computed run here in the real code).
          const virt: ChatScrollVirtualizer = {
            ...virtualizerNoOps(),
            ...makeStubVirtualizer(),
            updateViewport: () => { clock += 60 },
          }
          const hook = useChatScroll({ virtualizer: virt, messages })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          warn.mockClear()

          // A scroll of the reader: its refreshViewport runs the stalled render cascade.
          div.setScrollTop(20)
          hook.handlers.onScroll()

          expect(warn).toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('slow scroll phase'),
            expect.objectContaining({ phase: 'refreshViewport', ms: 60 }),
          )
          nowSpy.mockRestore()
          dispose()
          resolve()
        }
        catch (e) {
          nowSpy.mockRestore()
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('does not WARN when a large scroll phase stays within budget', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        let clock = 1000
        const nowSpy = vi.spyOn(performance, 'now').mockImplementation(() => clock)
        try {
          const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(0)
          const [messages] = createSignal<AgentChatMessage[]>([])
          // A normal (fast, sub-budget) render cascade: 10ms, under the 50ms threshold.
          const virt: ChatScrollVirtualizer = {
            ...virtualizerNoOps(),
            ...makeStubVirtualizer(),
            updateViewport: () => { clock += 10 },
          }
          const hook = useChatScroll({ virtualizer: virt, messages })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          warn.mockClear()

          div.setScrollTop(20)
          hook.handlers.onScroll()

          // Only the SLOW-PHASE warn is under test here.
          expect(warn).not.toHaveBeenCalledWith(
            '[chatScroll]',
            expect.stringContaining('slow scroll phase'),
            expect.anything(),
          )
          nowSpy.mockRestore()
          dispose()
          resolve()
        }
        catch (e) {
          nowSpy.mockRestore()
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))
})
