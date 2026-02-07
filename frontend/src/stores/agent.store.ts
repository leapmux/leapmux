import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'

interface AgentStoreState {
  agents: AgentInfo[]
  activeAgentId: string | null
}

export function createAgentStore() {
  const [state, setState] = createStore<AgentStoreState>({
    agents: [],
    activeAgentId: null,
  })

  return {
    state,

    setAgents(agents: AgentInfo[]) {
      setState('agents', agents)
    },

    addAgent(agent: AgentInfo) {
      setState('agents', prev => [...prev, agent])
      setState('activeAgentId', agent.id)
    },

    removeAgent(id: string) {
      setState('agents', prev => prev.filter(a => a.id !== id))
      if (state.activeAgentId === id) {
        setState('activeAgentId', state.agents[0]?.id ?? null)
      }
    },

    updateAgent(id: string, updates: Partial<AgentInfo>) {
      setState('agents', a => a.id === id, updates)
    },

    setActiveAgent(id: string | null) {
      setState('activeAgentId', id)
    },

    clear() {
      setState('agents', [])
      setState('activeAgentId', null)
    },
  }
}
