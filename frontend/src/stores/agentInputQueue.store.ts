import type { AgentInputQueueSnapshot } from '~/generated/proto/leapmux/v1/agent_pb'
import { createStore, produce } from 'solid-js/store'

interface AgentInputQueueState {
  byAgent: Record<string, AgentInputQueueSnapshot>
}

export function createAgentInputQueueStore() {
  const [state, setState] = createStore<AgentInputQueueState>({ byAgent: {} })

  return {
    state,

    apply(snapshot: AgentInputQueueSnapshot | undefined): boolean {
      if (!snapshot?.agentId)
        return false
      const current = state.byAgent[snapshot.agentId]
      if (current && current.revision > snapshot.revision)
        return false
      setState('byAgent', snapshot.agentId, snapshot)
      return true
    },

    get(agentId: string): AgentInputQueueSnapshot | undefined {
      return state.byAgent[agentId]
    },

    clearAgent(agentId: string) {
      setState('byAgent', produce((queues) => {
        delete queues[agentId]
      }))
    },

    clearAll() {
      setState('byAgent', {})
    },
  }
}
