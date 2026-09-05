import { createStore, produce } from 'solid-js/store'

export interface ControlRequest {
  requestId: string
  agentId: string
  payload: Record<string, unknown>
  // Per-instance token minted by the worker (AgentControlRequest.claim_token). The answer echoes it
  // in SendControlResponseRequest so the worker's idempotency claim dedups a reused request_id per
  // INSTANCE. The real ingestion always sets it (from the event); optional so synthetic fixtures and a
  // pre-token worker can omit it, in which case the answer degrades to request_id-only dedup.
  claimToken?: string
}

/**
 * The request id, made unique per INSTANCE by the worker's claim token.
 *
 * A request_id alone is not unique: the agent reuses one across a restart, and
 * Claude reuses one for a revised prompt after feedback, so this store can hold
 * two instances of one id at the same time (see `respondedKeys` below). Any
 * per-request state that outlives its request -- a saved draft, a saved answer
 * -- must key on this value. A key on the id alone hands the second instance
 * what the user already answered for the first, and one Submit sends it.
 *
 * A synthetic or pre-token request carries no token and keeps the bare id,
 * which is the key it had before the token existed.
 */
export function requestInstanceId(request: ControlRequest): string {
  return request.claimToken ? `${request.requestId}:${request.claimToken}` : request.requestId
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
  // re-add a request the user already handled. request_id alone is NOT globally
  // unique -- the agent reuses one across a restart (a JSON-RPC / plan-mode counter
  // that resets) and Claude reuses one for a revised ExitPlanMode prompt after
  // feedback -- so the key is scoped to the request INSTANCE by its claimToken
  // (the same per-instance token the worker keys its answer claim on). A genuine
  // re-ask of the SAME request_id carries a FRESH claimToken, so it is shown rather
  // than suppressed as a duplicate of the answered prior instance; a reconnect
  // replay of the SAME instance carries the SAME token and is still suppressed.
  // Only a pre-token / synthetic row (no claimToken) falls back to the payload
  // fingerprint, preserving the old "a revised prompt differs by payload" behavior.
  // Insertion order drives LRU eviction. Non-reactive and intentionally survives
  // clearAgent / clearAll.
  const respondedKeys = new Set<string>()
  const MAX_RESPONDED = 100

  function respondedKey(agentId: string, requestId: string, claimToken: string | undefined, payloadFp: string): string {
    // The `t:` / `p:` discriminator keeps a token-keyed entry from ever colliding with a
    // fingerprint-keyed one for the same (agent, request_id).
    return claimToken
      ? `${agentId}:${requestId}:t:${claimToken}`
      : `${agentId}:${requestId}:p:${payloadFp}`
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
      if (respondedKeys.has(respondedKey(agentId, request.requestId, request.claimToken, fp)))
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
        rememberResponded(respondedKey(agentId, requestId, removed.claimToken, canonicalJSON(removed.payload)))
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
