import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createAgentStore } from '~/stores/agent.store'

function makeAgent(id: string, workspaceId = 'ws1'): AgentInfo {
  return {
    $typeName: 'leapmux.v1.AgentInfo',
    id,
    workspaceId,
    title: `Agent ${id}`,
    model: 'sonnet',
    status: 'active',
    createdAt: '',
    closedAt: '',
  }
}

describe('createAgentStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createAgentStore()
      expect(store.state.agents).toEqual([])
      expect(store.state.activeAgentId).toBeNull()
      dispose()
    })
  })

  it('should add agent and set it as active', () => {
    createRoot((dispose) => {
      const store = createAgentStore()
      const agent = makeAgent('a1')
      store.addAgent(agent)
      expect(store.state.agents).toHaveLength(1)
      expect(store.state.agents[0].id).toBe('a1')
      expect(store.state.activeAgentId).toBe('a1')
      dispose()
    })
  })

  it('should remove agent and update active', () => {
    createRoot((dispose) => {
      const store = createAgentStore()
      store.addAgent(makeAgent('a1'))
      store.addAgent(makeAgent('a2'))
      expect(store.state.activeAgentId).toBe('a2')
      store.removeAgent('a2')
      expect(store.state.agents).toHaveLength(1)
      expect(store.state.activeAgentId).toBe('a1')
      dispose()
    })
  })

  it('should set active agent', () => {
    createRoot((dispose) => {
      const store = createAgentStore()
      store.addAgent(makeAgent('a1'))
      store.addAgent(makeAgent('a2'))
      store.setActiveAgent('a1')
      expect(store.state.activeAgentId).toBe('a1')
      dispose()
    })
  })

  it('should clear all agents', () => {
    createRoot((dispose) => {
      const store = createAgentStore()
      store.addAgent(makeAgent('a1'))
      store.addAgent(makeAgent('a2'))
      store.clear()
      expect(store.state.agents).toEqual([])
      expect(store.state.activeAgentId).toBeNull()
      dispose()
    })
  })
})
