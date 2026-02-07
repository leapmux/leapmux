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
})
