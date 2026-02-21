import { createStore, produce } from 'solid-js/store'

export interface ControlRequest {
  requestId: string
  agentId: string
  payload: Record<string, unknown>
}

interface ControlStoreState {
  pendingByAgent: Record<string, ControlRequest[]>
}

export function createControlStore() {
  const [state, setState] = createStore<ControlStoreState>({
    pendingByAgent: {},
  })

  // Track recently-responded-to request IDs so that a post-response reconnect
  // cannot re-add a request the user already handled. This is a plain Set
  // (not reactive) and intentionally survives clearAgent()/clearAll().
  const respondedIds = new Set<string>()
  const MAX_RESPONDED = 100

  return {
    state,

    addRequest(agentId: string, request: ControlRequest) {
      if (respondedIds.has(request.requestId))
        return
      setState(produce((s) => {
        if (!s.pendingByAgent[agentId]) {
          s.pendingByAgent[agentId] = []
        }
        if (s.pendingByAgent[agentId].some(r => r.requestId === request.requestId)) {
          return
        }
        s.pendingByAgent[agentId].push(request)
      }))
    },

    removeRequest(agentId: string, requestId: string) {
      respondedIds.add(requestId)
      if (respondedIds.size > MAX_RESPONDED) {
        respondedIds.clear()
        respondedIds.add(requestId)
      }
      setState(produce((s) => {
        const list = s.pendingByAgent[agentId]
        if (!list)
          return
        const idx = list.findIndex(r => r.requestId === requestId)
        if (idx !== -1) {
          list.splice(idx, 1)
        }
      }))
    },

    getRequests(agentId: string): ControlRequest[] {
      return state.pendingByAgent[agentId] ?? []
    },

    clearAgent(agentId: string) {
      setState('pendingByAgent', agentId, [])
    },

    clearAll() {
      setState('pendingByAgent', {})
    },
  }
}
