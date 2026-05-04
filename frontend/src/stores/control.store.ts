import { createStore, produce } from 'solid-js/store'

export interface ControlRequest {
  requestId: string
  agentId: string
  payload: Record<string, unknown>
}

interface ControlStoreState {
  pendingByAgent: Record<string, ControlRequest[]>
}

// Canonical JSON with sorted object keys, used as a payload fingerprint for
// dedup. The control-request payloads are JSON-decoded from the wire, so
// `undefined`/functions/symbols cannot appear and `JSON.stringify` always
// returns a string.
function canonicalJSON(value: unknown): string {
  if (value === null || typeof value !== 'object')
    return JSON.stringify(value)

  if (Array.isArray(value))
    return `[${value.map(canonicalJSON).join(',')}]`

  const obj = value as Record<string, unknown>
  return `{${Object.keys(obj).sort().map(key => `${JSON.stringify(key)}:${canonicalJSON(obj[key])}`).join(',')}}`
}

export function createControlStore() {
  const [state, setState] = createStore<ControlStoreState>({
    pendingByAgent: {},
  })

  // Track recently-responded-to requests so a post-response reconnect cannot
  // re-add a request the user already handled. Include a payload fingerprint:
  // request_id alone is not globally unique, and Claude can reuse one within
  // the same agent for a revised ExitPlanMode prompt after feedback.
  // Insertion order drives LRU eviction. Non-reactive and intentionally
  // survives clearAgent / clearAll.
  const respondedKeys = new Set<string>()
  const MAX_RESPONDED = 100

  function respondedKey(agentId: string, requestId: string, payloadFp: string): string {
    return `${agentId}:${requestId}:${payloadFp}`
  }

  function rememberResponded(key: string) {
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
  }

  return {
    state,

    addRequest(agentId: string, request: ControlRequest) {
      const fp = canonicalJSON(request.payload)
      if (respondedKeys.has(respondedKey(agentId, request.requestId, fp)))
        return
      setState(produce((s) => {
        const list = s.pendingByAgent[agentId] ??= []
        if (list.some(r => r.requestId === request.requestId && canonicalJSON(r.payload) === fp))
          return
        list.push(request)
      }))
    },

    removeRequest(agentId: string, requestId: string) {
      let removed: ControlRequest | undefined
      setState(produce((s) => {
        const list = s.pendingByAgent[agentId]
        if (!list)
          return
        const idx = list.findIndex(r => r.requestId === requestId)
        if (idx === -1)
          return
        removed = list[idx]
        list.splice(idx, 1)
      }))
      if (removed)
        rememberResponded(respondedKey(agentId, requestId, canonicalJSON(removed.payload)))
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
