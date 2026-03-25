import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'

function cacheLabels(agents: AgentInfo[]) {
  for (const a of agents) {
    if ((a.availableModels && a.availableModels.length > 0) || (a.availableOptionGroups && a.availableOptionGroups.length > 0))
      updateSettingsLabelCache(a.availableModels, a.availableOptionGroups)
  }
}

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
      cacheLabels(agents)
    },

    addAgent(agent: AgentInfo) {
      setState('agents', prev => [...prev, agent])
      setState('activeAgentId', agent.id)
      cacheLabels([agent])
    },

    removeAgent(id: string) {
      setState('agents', prev => prev.filter(a => a.id !== id))
      if (state.activeAgentId === id) {
        setState('activeAgentId', state.agents[0]?.id ?? null)
      }
    },

    updateAgent(id: string, updates: Partial<AgentInfo>) {
      setState('agents', a => a.id === id, updates)
      if ((updates.availableModels && updates.availableModels.length > 0) || (updates.availableOptionGroups && updates.availableOptionGroups.length > 0))
        updateSettingsLabelCache(updates.availableModels, updates.availableOptionGroups)
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
