import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createChatPaginationPolicy } from './chatScrollPaginationPolicy'

function scrollElement(args: { scrollTop?: number, scrollHeight?: number, clientHeight?: number } = {}): HTMLDivElement {
  return {
    scrollTop: args.scrollTop ?? 0,
    scrollHeight: args.scrollHeight ?? 1_000,
    clientHeight: args.clientHeight ?? 200,
  } as HTMLDivElement
}

describe('createChatPaginationPolicy', () => {
  it('loads older history only for explicit top intent and preserves the viewport', () => {
    createRoot((dispose) => {
      const preserve = vi.fn()
      const clearRestoreSuppression = vi.fn()
      const loadOlder = vi.fn()
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement({ scrollTop: 0 }),
        hasOlder: () => true,
        fetchingOlder: () => false,
        hasNewer: () => false,
        fetchingNewer: () => false,
        geomVersion: () => 0,
        isAtTopEdge: () => true,
        isAtBottomEdge: () => false,
        preserveBrowsingPosition: preserve,
        clearRestoreSuppression,
        onLoadOlder: loadOlder,
        onLoadNewer: vi.fn(),
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
      })

      expect(policy.tryLoadOlderOnExplicitTopIntent()).toBe(true)
      expect(preserve).toHaveBeenCalledOnce()
      expect(clearRestoreSuppression).toHaveBeenCalledOnce()
      expect(loadOlder).toHaveBeenCalledOnce()
      dispose()
    })
  })

  it('does not fetch an edge that is absent or already loading', () => {
    createRoot((dispose) => {
      const loadOlder = vi.fn()
      const loadNewer = vi.fn()
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement(),
        hasOlder: () => false,
        fetchingOlder: () => false,
        hasNewer: () => true,
        fetchingNewer: () => true,
        geomVersion: () => 0,
        isAtTopEdge: () => true,
        isAtBottomEdge: () => true,
        preserveBrowsingPosition: vi.fn(),
        clearRestoreSuppression: vi.fn(),
        onLoadOlder: loadOlder,
        onLoadNewer: loadNewer,
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
      })

      expect(policy.tryLoadOlderOnExplicitTopIntent()).toBe(false)
      expect(policy.tryLoadNewerOnExplicitBottomIntent()).toBe(false)
      expect(loadOlder).not.toHaveBeenCalled()
      expect(loadNewer).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('loads newer history for explicit bottom intent', () => {
    createRoot((dispose) => {
      const loadNewer = vi.fn()
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement(),
        hasOlder: () => false,
        fetchingOlder: () => false,
        hasNewer: () => true,
        fetchingNewer: () => false,
        geomVersion: () => 0,
        isAtTopEdge: () => false,
        isAtBottomEdge: () => true,
        preserveBrowsingPosition: vi.fn(),
        clearRestoreSuppression: vi.fn(),
        onLoadOlder: vi.fn(),
        onLoadNewer: loadNewer,
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
      })

      expect(policy.tryLoadNewerOnExplicitBottomIntent()).toBe(true)
      expect(loadNewer).toHaveBeenCalledOnce()
      expect(policy.bufferTargetPx()).toBe(600)
      dispose()
    })
  })

  it('updates stall state from reactive fetch state and live edge geometry', () => {
    createRoot((dispose) => {
      const [fetching, setFetching] = createSignal(false)
      const [geometry, setGeometry] = createSignal(0)
      let atTop = true
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement(),
        hasOlder: () => true,
        fetchingOlder: fetching,
        hasNewer: () => false,
        fetchingNewer: () => false,
        geomVersion: geometry,
        isAtTopEdge: () => atTop,
        isAtBottomEdge: () => false,
        preserveBrowsingPosition: vi.fn(),
        clearRestoreSuppression: vi.fn(),
        onLoadOlder: vi.fn(),
        onLoadNewer: vi.fn(),
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
      })

      expect(policy.stalledOlder()).toBe(false)
      setFetching(true)
      expect(policy.stalledOlder()).toBe(true)
      atTop = false
      setGeometry(value => value + 1)
      expect(policy.stalledOlder()).toBe(false)
      dispose()
    })
  })

  it('suppresses speculative older prefetch only at a scrollable live tail', () => {
    createRoot((dispose) => {
      let element = scrollElement({ scrollHeight: 1_000, clientHeight: 200 })
      const policy = createChatPaginationPolicy({
        getEl: () => element,
        hasOlder: () => true,
        fetchingOlder: () => false,
        hasNewer: () => false,
        fetchingNewer: () => false,
        geomVersion: () => 0,
        isAtTopEdge: () => false,
        isAtBottomEdge: () => true,
        preserveBrowsingPosition: vi.fn(),
        clearRestoreSuppression: vi.fn(),
        onLoadOlder: vi.fn(),
        onLoadNewer: vi.fn(),
        isAtBottom: () => true,
        isFollowing: () => true,
        anchorAtViewportTop: () => ({ id: 'm1', offsetWithinRow: 0 }),
        bufferScreens: 3,
      })

      expect(policy.suppressOlderPrefetchAtLiveTail()).toBe(true)
      element = scrollElement({ scrollHeight: 220, clientHeight: 200 })
      expect(policy.suppressOlderPrefetchAtLiveTail()).toBe(false)
      dispose()
    })
  })

  it('suppresses older prefetch while follow mode owns an anchorable position', () => {
    createRoot((dispose) => {
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement(),
        hasOlder: () => true,
        fetchingOlder: () => false,
        hasNewer: () => true,
        fetchingNewer: () => false,
        geomVersion: () => 0,
        isAtTopEdge: () => false,
        isAtBottomEdge: () => false,
        preserveBrowsingPosition: vi.fn(),
        clearRestoreSuppression: vi.fn(),
        onLoadOlder: vi.fn(),
        onLoadNewer: vi.fn(),
        isAtBottom: () => false,
        isFollowing: () => true,
        anchorAtViewportTop: () => ({ id: 'm1', offsetWithinRow: 0 }),
        bufferScreens: 3,
      })

      expect(policy.suppressOlderPrefetchAtLiveTail()).toBe(true)
      dispose()
    })
  })
})
