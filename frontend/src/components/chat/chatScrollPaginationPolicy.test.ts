import type { ChatPaginationPolicyDeps } from './chatScrollPaginationPolicy'
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
  it('loads older history only for explicit top intent', () => {
    createRoot((dispose) => {
      const loadOlder = vi.fn()
      let atTop = false
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement({ scrollTop: 0 }),
        hasOlder: () => true,
        fetchingOlder: () => false,
        hasNewer: () => false,
        fetchingNewer: () => false,
        geomVersion: () => 0,
        isAtTopEdge: () => atTop,
        isAtBottomEdge: () => false,
        onLoadOlder: loadOlder,
        onLoadNewer: vi.fn(),
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
        stagedOlder: () => false,
        stagedNewer: () => false,
        awaitingFirstView: () => false,
      })

      // Away from the top edge, the intent loads nothing, although older history exists.
      expect(policy.tryLoadOlderOnExplicitTopIntent()).toBe(false)
      expect(loadOlder).not.toHaveBeenCalled()

      atTop = true
      expect(policy.tryLoadOlderOnExplicitTopIntent()).toBe(true)
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
        onLoadOlder: loadOlder,
        onLoadNewer: loadNewer,
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
        stagedOlder: () => false,
        stagedNewer: () => false,
        awaitingFirstView: () => false,
      })

      expect(policy.tryLoadOlderOnExplicitTopIntent()).toBe(false)
      expect(policy.tryLoadNewerOnExplicitBottomIntent()).toBe(false)
      expect(loadOlder).not.toHaveBeenCalled()
      expect(loadNewer).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('loads newer history only for explicit bottom intent', () => {
    createRoot((dispose) => {
      const loadNewer = vi.fn()
      let atBottomEdge = false
      const policy = createChatPaginationPolicy({
        getEl: () => scrollElement(),
        hasOlder: () => false,
        fetchingOlder: () => false,
        hasNewer: () => true,
        fetchingNewer: () => false,
        geomVersion: () => 0,
        isAtTopEdge: () => false,
        isAtBottomEdge: () => atBottomEdge,
        onLoadOlder: vi.fn(),
        onLoadNewer: loadNewer,
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
        stagedOlder: () => false,
        stagedNewer: () => false,
        awaitingFirstView: () => false,
      })

      // Away from the bottom edge, the intent loads nothing, although newer history exists.
      expect(policy.tryLoadNewerOnExplicitBottomIntent()).toBe(false)
      expect(loadNewer).not.toHaveBeenCalled()

      atBottomEdge = true
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
        onLoadOlder: vi.fn(),
        onLoadNewer: vi.fn(),
        isAtBottom: () => false,
        isFollowing: () => false,
        anchorAtViewportTop: () => null,
        bufferScreens: 3,
        stagedOlder: () => false,
        stagedNewer: () => false,
        awaitingFirstView: () => false,
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
        onLoadOlder: vi.fn(),
        onLoadNewer: vi.fn(),
        isAtBottom: () => true,
        isFollowing: () => true,
        anchorAtViewportTop: () => ({ id: 'm1', offsetWithinRow: 0 }),
        bufferScreens: 3,
        stagedOlder: () => false,
        stagedNewer: () => false,
        awaitingFirstView: () => false,
      })

      expect(policy.suppressOlderPrefetchAtLiveTail()).toBe(true)
      // A range of 2px cannot be scrolled: the page still needs older rows to become scrollable.
      element = scrollElement({ scrollHeight: 202, clientHeight: 200 })
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
        onLoadOlder: vi.fn(),
        onLoadNewer: vi.fn(),
        isAtBottom: () => false,
        isFollowing: () => true,
        anchorAtViewportTop: () => ({ id: 'm1', offsetWithinRow: 0 }),
        bufferScreens: 3,
        stagedOlder: () => false,
        stagedNewer: () => false,
        awaitingFirstView: () => false,
      })

      expect(policy.suppressOlderPrefetchAtLiveTail()).toBe(true)
      dispose()
    })
  })

  /**
   * A policy for the cases of staged rows: both sides have more history, no fetch runs, and
   * the view is at both edges. `overrides` replace single deps.
   */
  function stagedPolicy(overrides: Partial<ChatPaginationPolicyDeps> = {}) {
    const loadOlder = vi.fn()
    const loadNewer = vi.fn()
    const policy = createChatPaginationPolicy({
      getEl: () => scrollElement(),
      hasOlder: () => true,
      fetchingOlder: () => false,
      hasNewer: () => true,
      fetchingNewer: () => false,
      geomVersion: () => 0,
      isAtTopEdge: () => true,
      isAtBottomEdge: () => true,
      onLoadOlder: loadOlder,
      onLoadNewer: loadNewer,
      isAtBottom: () => false,
      isFollowing: () => false,
      anchorAtViewportTop: () => null,
      bufferScreens: 3,
      stagedOlder: () => false,
      stagedNewer: () => false,
      awaitingFirstView: () => false,
      ...overrides,
    })
    return { policy, loadOlder, loadNewer }
  }

  it('loads no older page while rows are staged above', () => {
    createRoot((dispose) => {
      let staged = true
      const { policy, loadOlder } = stagedPolicy({
        stagedOlder: () => staged,
      })

      expect(policy.loadOlderMessages()).toBe(false)
      expect(policy.tryLoadOlderOnExplicitTopIntent()).toBe(false)
      expect(loadOlder).not.toHaveBeenCalled()

      staged = false
      expect(policy.loadOlderMessages()).toBe(true)
      expect(loadOlder).toHaveBeenCalledOnce()
      dispose()
    })
  })

  it('reports the staged top edge, and no stall, while rows are staged above a view at the top', () => {
    createRoot((dispose) => {
      const { policy } = stagedPolicy({
        stagedOlder: () => true,
      })

      expect(policy.atStagedTopEdge()).toBe(true)
      expect(policy.stalledOlder()).toBe(false)
      dispose()
    })
  })

  it('reports no staged top edge while no row is in the list', () => {
    createRoot((dispose) => {
      const { policy } = stagedPolicy({
        stagedOlder: () => true,
        awaitingFirstView: () => true,
      })

      expect(policy.atStagedTopEdge()).toBe(false)
      dispose()
    })
  })

  it('reports no staged top edge while the view is away from the top', () => {
    createRoot((dispose) => {
      const { policy } = stagedPolicy({
        stagedOlder: () => true,
        isAtTopEdge: () => false,
      })

      expect(policy.atStagedTopEdge()).toBe(false)
      dispose()
    })
  })

  it('reports the staged bottom edge, and loads no newer page, while rows are staged below', () => {
    createRoot((dispose) => {
      const { policy, loadNewer } = stagedPolicy({
        stagedNewer: () => true,
      })

      expect(policy.atStagedBottomEdge()).toBe(true)
      expect(policy.stalledNewer()).toBe(false)
      expect(policy.loadNewerMessages()).toBe(false)
      expect(policy.tryLoadNewerOnExplicitBottomIntent()).toBe(false)
      expect(loadNewer).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('reports no staged bottom edge for a follower', () => {
    createRoot((dispose) => {
      const { policy } = stagedPolicy({
        stagedNewer: () => true,
        isFollowing: () => true,
      })

      expect(policy.atStagedBottomEdge()).toBe(false)
      dispose()
    })
  })

  it('ends the staged top edge when the staged rows join, with no scroll event', () => {
    createRoot((dispose) => {
      const [staged, setStaged] = createSignal(true)
      const { policy } = stagedPolicy({ stagedOlder: staged })
      expect(policy.atStagedTopEdge()).toBe(true)

      // No change of the geometry version: the staged state alone runs the memo again.
      setStaged(false)

      expect(policy.atStagedTopEdge()).toBe(false)
      dispose()
    })
  })

  it('reports a stall for a fetch at the edge with no staged row', () => {
    createRoot((dispose) => {
      const { policy } = stagedPolicy({
        fetchingOlder: () => true,
      })

      expect(policy.stalledOlder()).toBe(true)
      expect(policy.atStagedTopEdge()).toBe(false)
      dispose()
    })
  })
})
