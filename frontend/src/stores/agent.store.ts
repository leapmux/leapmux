import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'

function cacheLabels(agents: AgentInfo[]) {
  for (const a of agents) {
    if ((a.availableModels && a.availableModels.length > 0) || (a.availableOptionGroups && a.availableOptionGroups.length > 0))
      updateSettingsLabelCache(a.availableModels, a.availableOptionGroups)
  }
}

/**
 * Shallow-equal comparison tuned for AgentInfo fields. Primitive values
 * use Object.is. For object values (e.g. gitStatus) we shallow-compare
 * sub-fields so a fresh proto instance carrying the same data is treated
 * as unchanged — proto decoding allocates a new object every time.
 */
function agentFieldEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b))
    return true
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object')
    return false
  if (Array.isArray(a) || Array.isArray(b))
    return false
  const aKeys = Object.keys(a as object)
  const bKeys = Object.keys(b as object)
  if (aKeys.length !== bKeys.length)
    return false
  for (const k of aKeys) {
    if (!Object.is((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]))
      return false
  }
  return true
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
      const existing = state.agents.find(a => a.id === id)
      if (!existing) {
        // Buffer until addAgent is called — avoids losing the transition
        // from STARTING → ACTIVE when the broadcast overtakes ListAgents.
        pendingUpdates.set(id, mergePartial(pendingUpdates.get(id) ?? {}, updates))
        return
      }
      // Filter out no-op fields so setState doesn't notify subscribers
      // for values that didn't actually change. statusChange events carry
      // the full snapshot on every turn boundary; without this guard,
      // every field's reactive readers re-run even when only one changed.
      const changed: Partial<AgentInfo> = {}
      let hasChange = false
      for (const key of Object.keys(updates) as (keyof AgentInfo)[]) {
        const next = updates[key]
        if (!agentFieldEqual(existing[key], next)) {
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
