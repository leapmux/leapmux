import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { shallowEqual } from '~/lib/shallowEqual'

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
  //
  // Why: WatchEvents catch-up replay covers most cases, but live
  // statusChange events can arrive in the window between ListAgents
  // starting and its result being applied via setAgents/addAgent. Folding
  // this into a "call addAgent with synthesized AgentInfo" would require
  // inventing workspaceId/workerId/workingDir/createdAt at statusChange
  // time, so the parallel Map stays.
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
      const existing = state.agents.find(a => a.id === id)
      if (!existing) {
        // Buffer the update: statusChange broadcasts can overtake a ListAgents
        // fetch, and losing them would strand the agent in STARTING state.
        pendingUpdates.set(id, mergePartial(pendingUpdates.get(id) ?? {}, updates))
        return
      }
      // statusChange carries the full snapshot on every turn boundary; skip
      // fields that didn't actually change so reactive readers don't re-run.
      const changed: Partial<AgentInfo> = {}
      let hasChange = false
      for (const key of Object.keys(updates) as (keyof AgentInfo)[]) {
        const next = updates[key]
        if (!shallowEqual(existing[key], next)) {
          ;(changed as Record<string, unknown>)[key] = next
          hasChange = true
        }
      }
      if (!hasChange)
        return
      setState('agents', a => a.id === id, changed)
      if ((changed.availableModels && changed.availableModels.length > 0) || (changed.availableOptionGroups && changed.availableOptionGroups.length > 0))
        updateSettingsLabelCache(changed.availableModels, changed.availableOptionGroups)
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
