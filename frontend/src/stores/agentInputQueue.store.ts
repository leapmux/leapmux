import type { AgentInputQueueSnapshot } from '~/generated/proto/leapmux/v1/agent_pb'
import { createStore, produce } from 'solid-js/store'

interface AgentInputQueueState {
  byAgent: Record<string, AgentInputQueueSnapshot>
}

/**
 * Create the authoritative queue store. The live-agent check rejects an event
 * that arrives after a local or remote tab close removed its queue entry.
 */
export function createAgentInputQueueStore(isLiveAgent: (agentId: string) => boolean = () => true) {
  const [state, setState] = createStore<AgentInputQueueState>({ byAgent: {} })
  const applyListeners = new Set<(agentId: string, snapshot: AgentInputQueueSnapshot) => void>()

  return {
    state,

    /**
     * Call `listener` with each snapshot that `apply` stores, whatever brought
     * it: an RPC response or a broadcast. Returns the function that removes
     * the listener.
     */
    onApply(listener: (agentId: string, snapshot: AgentInputQueueSnapshot) => void): () => void {
      applyListeners.add(listener)
      return () => applyListeners.delete(listener)
    },

    /** Apply `agentId`'s snapshot. The carrier states the agent, as the snapshot does not. */
    apply(agentId: string, snapshot: AgentInputQueueSnapshot | undefined): boolean {
      if (!agentId || !snapshot)
        return false
      if (!isLiveAgent(agentId))
        return false
      const current = state.byAgent[agentId]
      if (current && current.revision > snapshot.revision)
        return false
      // Solid merges object values passed to a path setter. Replace the node
      // through a producer so omitted optional fields clear earlier values.
      setState('byAgent', produce((queues) => {
        queues[agentId] = snapshot
      }))
      for (const listener of applyListeners)
        listener(agentId, snapshot)
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
  }
}
