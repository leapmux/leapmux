import type { TurnEndRefreshGateDeps } from './turnEndRefresh'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createTurnEndRefreshGate, TURN_END_REFRESH_WINDOW_MS } from './turnEndRefresh'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

const SHOWN_WORKER = 'w-shown'

function makeGate(overrides: Partial<TurnEndRefreshGateDeps> = {}) {
  const refresh = vi.fn()
  const gate = createTurnEndRefreshGate({
    shownWorkerId: () => SHOWN_WORKER,
    workerIdForAgent: () => SHOWN_WORKER,
    isAgentClosing: () => false,
    refresh,
    ...overrides,
  })
  return { gate, refresh }
}

describe('createTurnEndRefreshGate', () => {
  describe('relevance', () => {
    it('refreshes for an agent on the worker the panels show', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('drops a turn end from another worker', () => {
      // Another machine entirely: nothing it wrote is in this tree.
      const { gate, refresh } = makeGate({ workerIdForAgent: () => 'w-other' })
      gate.notify('a-1')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS * 2)

      expect(refresh).not.toHaveBeenCalled()
    })

    it('refreshes when the panels resolve no worker', () => {
      const { gate, refresh } = makeGate({ shownWorkerId: () => '' })
      gate.notify('a-1')

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('refreshes when the ending agent resolves no worker', () => {
      // An unhydrated tab reports none. Staying quiet there would hide a real
      // change behind a lookup that had not landed yet.
      const { gate, refresh } = makeGate({ workerIdForAgent: () => '' })
      gate.notify('a-1')

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('drops a turn end from a tab that closes', () => {
      const { gate, refresh } = makeGate({ isAgentClosing: id => id === 'a-1' })
      gate.notify('a-1')

      expect(refresh).not.toHaveBeenCalled()
    })

    it('asks per agent, so a closing sibling does not silence the others', () => {
      const { gate, refresh } = makeGate({ isAgentClosing: id => id === 'a-1' })
      gate.notify('a-1')
      gate.notify('a-2')

      expect(refresh).toHaveBeenCalledTimes(1)
    })
  })

  describe('the trailing edge asks again', () => {
    it('drops the trailing refresh when every admitted tab closed meanwhile', () => {
      // The whole window sits between the admission and the refresh it pays
      // for. A tab torn down inside it has nothing left to refresh.
      const closing = new Set<string>()
      const { gate, refresh } = makeGate({ isAgentClosing: id => closing.has(id) })
      gate.notify('a-1')
      gate.notify('a-2')
      expect(refresh).toHaveBeenCalledTimes(1)

      closing.add('a-1')
      closing.add('a-2')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS)

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('drops the trailing refresh when the user moves to another worker', () => {
      let shown = SHOWN_WORKER
      const { gate, refresh } = makeGate({ shownWorkerId: () => shown })
      gate.notify('a-1')
      gate.notify('a-2')

      shown = 'w-elsewhere'
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS)

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('keeps the trailing refresh when one tab of the burst is still live', () => {
      // `a-1` leads and is spent there, so the trailing edge asks about the two
      // that arrived inside the window. One survivor is enough.
      const closing = new Set<string>()
      const { gate, refresh } = makeGate({ isAgentClosing: id => closing.has(id) })
      gate.notify('a-1')
      gate.notify('a-2')
      gate.notify('a-3')

      closing.add('a-2')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS)

      expect(refresh).toHaveBeenCalledTimes(2)
    })

    it('leads again right after a trailing edge that spent nothing', () => {
      // The window exists to coalesce a burst. One that turns out to hold
      // nothing must not delay the next real turn end behind it.
      const closing = new Set<string>()
      const { gate, refresh } = makeGate({ isAgentClosing: id => closing.has(id) })
      gate.notify('a-1')
      gate.notify('a-2')

      closing.add('a-2')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS) // trailing edge, nothing live
      expect(refresh).toHaveBeenCalledTimes(1)

      gate.notify('a-3')

      expect(refresh).toHaveBeenCalledTimes(2)
    })

    it('opens a new window after a trailing refresh that DID spend', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')
      gate.notify('a-2')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS) // trailing refresh runs
      expect(refresh).toHaveBeenCalledTimes(2)

      gate.notify('a-3')
      expect(refresh).toHaveBeenCalledTimes(2)
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS)
      expect(refresh).toHaveBeenCalledTimes(3)
    })

    it('forgets the burst after the trailing refresh, so it cannot pay twice', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')
      gate.notify('a-2')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS) // the trailing refresh runs
      expect(refresh).toHaveBeenCalledTimes(2)

      // A quiet window with nothing admitted must add nothing.
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS * 3)
      expect(refresh).toHaveBeenCalledTimes(2)
    })
  })

  describe('coalescing', () => {
    it('refreshes immediately on the first turn end', () => {
      // The common case is one turn end on its own, and the user notices a
      // delay there as a tree that lags behind the agent.
      const { gate, refresh } = makeGate()
      gate.notify('a-1')

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('costs exactly one refresh when nothing follows', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS * 3)

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('collapses a burst into two refreshes: the first and one trailing', () => {
      // A parent's turn end plus a fan-out of subagents finishing behind it.
      const { gate, refresh } = makeGate()
      gate.notify('root')
      for (const child of ['c-1', 'c-2', 'c-3', 'c-4', 'c-5'])
        gate.notify(child)

      expect(refresh).toHaveBeenCalledTimes(1)
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS)
      expect(refresh).toHaveBeenCalledTimes(2)
    })

    it('runs the trailing refresh after the burst, so it sees the last write', () => {
      const { gate, refresh } = makeGate()
      gate.notify('root')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS - 1)
      gate.notify('c-1')

      expect(refresh).toHaveBeenCalledTimes(1)
      vi.advanceTimersByTime(1)
      expect(refresh).toHaveBeenCalledTimes(2)
    })

    it('refreshes immediately again once a quiet window passes', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS)
      gate.notify('a-2')

      expect(refresh).toHaveBeenCalledTimes(2)
    })

    it('takes the window from the caller', () => {
      const { gate, refresh } = makeGate({ windowMs: 1000 })
      gate.notify('a-1')
      gate.notify('a-2')
      vi.advanceTimersByTime(999)
      expect(refresh).toHaveBeenCalledTimes(1)

      vi.advanceTimersByTime(1)
      expect(refresh).toHaveBeenCalledTimes(2)
    })
  })

  describe('dispose', () => {
    it('drops a pending trailing refresh', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')
      gate.notify('a-2')
      gate.dispose()
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS * 2)

      expect(refresh).toHaveBeenCalledTimes(1)
    })

    it('refuses a later notify, so a frame still in flight cannot re-arm it', () => {
      const { gate, refresh } = makeGate()
      gate.dispose()
      gate.notify('a-1')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS * 2)

      expect(refresh).not.toHaveBeenCalled()
    })

    it('stays disposed after a refresh already ran', () => {
      const { gate, refresh } = makeGate()
      gate.notify('a-1')
      gate.dispose()
      gate.notify('a-2')
      vi.advanceTimersByTime(TURN_END_REFRESH_WINDOW_MS * 2)

      expect(refresh).toHaveBeenCalledTimes(1)
    })
  })
})
