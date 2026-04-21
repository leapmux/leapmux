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

  // Buffered updateAgent calls for agent IDs the store doesn't know
  // about yet — applied in addAgent so status events that overtake the
  // OpenAgent RPC response (or a ListAgents fetch) are not lost.
  const pendingUpdates = new Map<string, Partial<AgentInfo>>()

  function mergePartial(a: Partial<AgentInfo>, b: Partial<AgentInfo>): Partial<AgentInfo> {
    return { ...a, ...b }
  }

  return {
    state,

    setAgents(agents: AgentInfo[]) {
      setState('agents', agents)
      cacheLabels(agents)
    },

    addAgent(agent: AgentInfo) {
      // Apply any pre-arrived updates for this ID before inserting.
      const buffered = pendingUpdates.get(agent.id)
      const merged = buffered ? { ...agent, ...buffered } as AgentInfo : agent
      if (buffered)
        pendingUpdates.delete(agent.id)
      setState('agents', prev => [...prev, merged])
      setState('activeAgentId', merged.id)
      cacheLabels([merged])
    },

    removeAgent(id: string) {
      pendingUpdates.delete(id)
      setState('agents', prev => prev.filter(a => a.id !== id))
      if (state.activeAgentId === id) {
        setState('activeAgentId', state.agents[0]?.id ?? null)
      }
    },

    updateAgent(id: string, updates: Partial<AgentInfo>) {
      const exists = state.agents.some(a => a.id === id)
      if (!exists) {
        // Buffer until addAgent is called — avoids losing the transition
        // from STARTING → ACTIVE when the broadcast overtakes ListAgents.
        pendingUpdates.set(id, mergePartial(pendingUpdates.get(id) ?? {}, updates))
        return
      }
      setState('agents', a => a.id === id, updates)
      if ((updates.availableModels && updates.availableModels.length > 0) || (updates.availableOptionGroups && updates.availableOptionGroups.length > 0))
        updateSettingsLabelCache(updates.availableModels, updates.availableOptionGroups)
    },

    setActiveAgent(id: string | null) {
      setState('activeAgentId', id)
    },

    clear() {
      pendingUpdates.clear()
      setState('agents', [])
      setState('activeAgentId', null)
    },
  }
}
