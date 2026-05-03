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

  // Track recently-responded-to requests so a post-response reconnect cannot
  // re-add a request the user already handled. Keyed by `${agentId}:${requestId}`
  // because request_id alone is not unique across agents — Codex and the
  // ACP-family providers (Cursor, OpenCode, Copilot, Pi) use small per-process
  // JSON-RPC ids that collide between sibling tabs. Insertion order drives
  // LRU eviction so overflow drops the oldest entries one-by-one instead of
  // wiping the whole set (which would briefly let a cancelled prompt re-add
  // itself if a stale replay landed during the gap). Non-reactive and
  // intentionally survives clearAgent / clearAll.
  const respondedKeys = new Set<string>()
  const MAX_RESPONDED = 100

  function respondedKey(agentId: string, requestId: string): string {
    return `${agentId}:${requestId}`
  }

  return {
    state,

    addRequest(agentId: string, request: ControlRequest) {
      if (respondedKeys.has(respondedKey(agentId, request.requestId)))
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
      const key = respondedKey(agentId, requestId)
      // delete-then-add bumps the entry to the most-recent insertion slot so
      // re-responding to the same key does not age it out prematurely.
      respondedKeys.delete(key)
      respondedKeys.add(key)
      while (respondedKeys.size > MAX_RESPONDED) {
        const oldest = respondedKeys.values().next().value
        if (oldest === undefined)
          break
        respondedKeys.delete(oldest)
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
