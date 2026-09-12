import type { AgentControlCancelRequest, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createStore, produce } from 'solid-js/store'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * Why `payload` is empty although the agent sent bytes.
 *
 * `malformed`: the bytes are not JSON at all.
 * `not-an-object`: the bytes are valid JSON, but a scalar, a null, or an array, and every
 * reader of a payload expects an object.
 */
export type ControlPayloadFault = 'malformed' | 'not-an-object'

export interface ControlRequest {
  requestId: string
  agentId: string
  agentSessionId?: string
  /** The provider from the control event remains available before agent metadata loads. */
  agentProvider?: AgentProvider
  payload: Record<string, unknown>
  /**
   * Set when LeapMux could not read the bytes into `payload`, which is then empty. The
   * request is still kept: the agent BLOCKS on an answer, so dropping it leaves a turn
   * that never ends and a reader with no question. `originalPayload` holds what arrived.
   */
  payloadFault?: ControlPayloadFault
  /** Original provider bytes remain available for copying and inspection. */
  originalPayload?: Uint8Array
  /** The exact transcript message that supplies omitted display fields. */
  sourceSeq?: bigint
  /** The worker's delivery evidence for this request instance. */
  responseState?: ControlResponseState
  /** The worker's instance token. A response echoes it to identify the exact request. */
  claimToken?: string
}

/**
 * Combine the request ID and worker token for drafts and response state.
 * Providers can reuse IDs. A new token keeps each instance separate.
 * A local request without a token uses its request ID.
 */
export function requestInstanceId(request: ControlRequest): string {
  return request.claimToken ? `${request.requestId}:${request.claimToken}` : request.requestId
}

/** Prefer the provider attached to this request over separately loaded agent metadata. */
export function controlRequestProvider(request: ControlRequest | null | undefined, fallback?: AgentProvider): AgentProvider | undefined {
  return request?.agentProvider ?? fallback
}

interface ControlStoreState {
  pendingByAgent: Record<string, ControlRequest[]>
}

function mergeResponseState(current: ControlResponseState | undefined, next: ControlResponseState): ControlResponseState {
  // A late announcement cannot undo confirmed delivery or reopen uncertain delivery.
  if (current === ControlResponseState.DELIVERED
    && next !== ControlResponseState.COMPLETED && next !== ControlResponseState.CANCELED) {
    return current
  }
  if (current === ControlResponseState.UNCERTAIN
    && next !== ControlResponseState.DELIVERED && next !== ControlResponseState.COMPLETED && next !== ControlResponseState.CANCELED) {
    return current
  }
  return next
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

  // Keep completed and canceled instances distinct. A late delivery receipt can restore a canceled prompt for recording.
  const removedInstances = new Map<string, ControlResponseState>()
  const MAX_REMOVED = 100

  function removalKey(agentId: string, requestId: string, claimToken: string | undefined, payloadFp: string): string {
    // The `t:` / `p:` discriminator keeps a token-keyed entry from ever colliding with a
    // fingerprint-keyed one for the same (agent, request_id).
    return claimToken
      ? `${agentId}:${requestId}:t:${claimToken}`
      : `${agentId}:${requestId}:p:${payloadFp}`
  }

  function rememberRemoval(key: string, state: ControlResponseState) {
    // delete-then-add bumps the entry to the most-recent insertion slot so
    // re-responding to the same key does not age it out prematurely.
    removedInstances.delete(key)
    removedInstances.set(key, state)
    while (removedInstances.size > MAX_REMOVED) {
      const oldest = removedInstances.keys().next().value
      if (oldest === undefined)
        break
      removedInstances.delete(oldest)
    }
  }

  function removeRequest(agentId: string, requestId: string, claimToken: string | undefined, responseState: ControlResponseState) {
    let removed: ControlRequest | undefined
    setState(produce((s) => {
      const list = s.pendingByAgent[agentId]
      const index = list?.findIndex(request => request.requestId === requestId && (request.claimToken ?? '') === (claimToken ?? '')) ?? -1
      if (index < 0 || !list)
        return
      removed = list[index]
      list.splice(index, 1)
    }))
    if (removed || claimToken) {
      const key = removalKey(agentId, requestId, claimToken, removed ? canonicalJSON(removed.payload) : '')
      if (removedInstances.get(key) !== ControlResponseState.COMPLETED)
        rememberRemoval(key, responseState)
    }
  }

  return {
    state,

    addRequest(agentId: string, request: ControlRequest) {
      const fp = canonicalJSON(request.payload)
      const key = removalKey(agentId, request.requestId, request.claimToken, fp)
      const previous = removedInstances.get(key)
      if (previous !== undefined && !(previous === ControlResponseState.CANCELED && request.responseState === ControlResponseState.DELIVERED))
        return
      removedInstances.delete(key)
      setState(produce((s) => {
        const list = s.pendingByAgent[agentId] ??= []
        const existing = list.find(r => r.requestId === request.requestId && (r.agentSessionId ?? '') === (request.agentSessionId ?? '') && r.claimToken === request.claimToken && canonicalJSON(r.payload) === fp)
        if (existing) {
          if (request.responseState !== undefined)
            existing.responseState = mergeResponseState(existing.responseState, request.responseState)
          if (existing.originalPayload === undefined && request.originalPayload !== undefined)
            existing.originalPayload = request.originalPayload.slice()
          if (existing.agentProvider === undefined && request.agentProvider !== undefined)
            existing.agentProvider = request.agentProvider
          if (!existing.sourceSeq && request.sourceSeq && request.sourceSeq > 0n)
            existing.sourceSeq = request.sourceSeq
          return
        }
        list.push({ ...request, originalPayload: request.originalPayload?.slice() })
      }))
    },

    setResponseState(request: ControlRequest, responseState: ControlResponseState) {
      setState(produce((s) => {
        const existing = s.pendingByAgent[request.agentId]?.find(item => item.requestId === request.requestId && item.claimToken === request.claimToken)
        if (existing)
          existing.responseState = mergeResponseState(existing.responseState, responseState)
      }))
    },

    removeRequest(agentId: string, requestId: string, claimToken?: string) {
      removeRequest(agentId, requestId, claimToken, ControlResponseState.COMPLETED)
    },

    cancelRequest(event: Pick<AgentControlCancelRequest, 'agentId' | 'requestId' | 'claimToken' | 'responseState'>) {
      let retainForRecording = false
      setState(produce((s) => {
        const request = s.pendingByAgent[event.agentId]?.find(request => request.requestId === event.requestId && request.claimToken === event.claimToken)
        retainForRecording = event.responseState !== ControlResponseState.COMPLETED
          && (event.responseState === ControlResponseState.DELIVERED || request?.responseState === ControlResponseState.DELIVERED)
        if (retainForRecording && request)
          request.responseState = ControlResponseState.DELIVERED
      }))
      if (retainForRecording)
        return
      removeRequest(event.agentId, event.requestId, event.claimToken, event.responseState === ControlResponseState.COMPLETED ? ControlResponseState.COMPLETED : ControlResponseState.CANCELED)
    },

    clearProviderRequests(agentId: string) {
      setState('pendingByAgent', agentId, (state.pendingByAgent[agentId] ?? []).filter(request => request.responseState === ControlResponseState.DELIVERED))
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
