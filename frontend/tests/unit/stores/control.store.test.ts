import type { ControlRequest } from '~/stores/control.store'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createControlStore } from '~/stores/control.store'

function makeRequest(requestId: string, agentId: string): ControlRequest {
  return { requestId, agentId, payload: { data: requestId } }
}

describe('createControlStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      expect(store.state.pendingByAgent).toEqual({})
      dispose()
    })
  })

  it('should create entry for new agent on addRequest', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      const req = makeRequest('r1', 'agent-1')
      store.addRequest('agent-1', req)
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0].requestId).toBe('r1')
      dispose()
    })
  })

  it('should append to existing agent on addRequest', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('r2', 'agent-1'))
      expect(store.getRequests('agent-1')).toHaveLength(2)
      expect(store.getRequests('agent-1')[0].requestId).toBe('r1')
      expect(store.getRequests('agent-1')[1].requestId).toBe('r2')
      dispose()
    })
  })

  it('should remove specific request on removeRequest', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('r2', 'agent-1'))
      store.removeRequest('agent-1', 'r1')
      expect(store.getRequests('agent-1')).toHaveLength(1)
      expect(store.getRequests('agent-1')[0].requestId).toBe('r2')
      dispose()
    })
  })

  it('should not crash on removeRequest with non-existent agent', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      expect(() => store.removeRequest('no-agent', 'r1')).not.toThrow()
      dispose()
    })
  })

  it('should not crash on removeRequest with non-existent requestId', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      expect(() => store.removeRequest('agent-1', 'nonexistent')).not.toThrow()
      expect(store.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should return empty array for unknown agent on getRequests', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      expect(store.getRequests('unknown-agent')).toEqual([])
      dispose()
    })
  })

  it('should clear all requests for an agent on clearAgent', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-1', makeRequest('r2', 'agent-1'))
      store.addRequest('agent-2', makeRequest('r3', 'agent-2'))
      store.clearAgent('agent-1')
      expect(store.getRequests('agent-1')).toEqual([])
      expect(store.getRequests('agent-2')).toHaveLength(1)
      dispose()
    })
  })

  it('should empty everything on clearAll', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.addRequest('agent-2', makeRequest('r2', 'agent-2'))
      store.clearAll()

      // After clearAll, new additions should work correctly
      store.addRequest('agent-3', makeRequest('r3', 'agent-3'))
      expect(store.getRequests('agent-3')).toHaveLength(1)
      expect(store.getRequests('agent-3')[0].requestId).toBe('r3')

      // Previously populated agents should not show new data
      // (a fresh store with only agent-3 added)
      dispose()
    })
  })

  // Two Codex / ACP-family tabs commonly produce identical request_ids
  // (the JSON-RPC id space is per-subprocess and starts low). Answering
  // one tab's prompt must not silently suppress the sibling's prompt.
  it('does not suppress a sibling agent request that shares a requestId', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-A', makeRequest('1', 'agent-A'))
      store.removeRequest('agent-A', '1')

      store.addRequest('agent-B', makeRequest('1', 'agent-B'))
      expect(store.getRequests('agent-B')).toHaveLength(1)
      expect(store.getRequests('agent-B')[0].requestId).toBe('1')
      dispose()
    })
  })

  // Stale replay protection still works within a single agent: a request
  // the user just answered must not redraw if the same row is replayed
  // before the backend's DELETE round-trip is observed by the new stream.
  it('keeps suppressing the same agent+requestId after a response', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      store.removeRequest('agent-1', 'r1')

      // Simulate a replayed controlRequest landing during a reconnect.
      store.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      expect(store.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  // The previous wipe-on-overflow behavior could let an answered prompt
  // re-add itself once 100 unrelated responses cleared the dedup set.
  // The LRU eviction must keep the most-recent entries protected even
  // after the cap is exceeded.
  it('preserves the most-recent suppressions after overflow', () => {
    createRoot((dispose) => {
      const store = createControlStore()
      store.addRequest('agent-1', makeRequest('keep', 'agent-1'))
      store.removeRequest('agent-1', 'keep')

      // Answer 200 unrelated requests across other agents to force eviction.
      for (let i = 0; i < 200; i++) {
        const id = `noise-${i}`
        store.addRequest(`agent-noise-${i}`, makeRequest(id, `agent-noise-${i}`))
        store.removeRequest(`agent-noise-${i}`, id)
      }

      // The most-recent suppressed key from the noise burst must still block.
      store.addRequest('agent-noise-199', makeRequest('noise-199', 'agent-noise-199'))
      expect(store.getRequests('agent-noise-199')).toHaveLength(0)
      dispose()
    })
  })
})
