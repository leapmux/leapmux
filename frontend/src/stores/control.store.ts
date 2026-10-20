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
   * request is still kept: a BLOCKING request makes the agent wait for an answer, so
   * dropping it leaves a turn that never ends and a reader with no question.
   * `originalPayload` holds what arrived.
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
  /**
   * True for a request that the agent does not wait for: the turn continues and the
   * question waits for a reader who returns later. Absent means a blocking request.
   */
  nonBlocking?: boolean
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

/**
 * The one rule for which request the composer shows: the head when it is blocking,
 * or when it is non-blocking and the reader opened the questions. The store keeps
 * every blocking request before every non-blocking one, so the head is blocking
 * whenever a blocking request exists.
 */
export function shownControlRequest(requests: readonly ControlRequest[], questionsOpen: boolean): ControlRequest | null {
  const head = requests[0]
  if (!head)
    return null
  if (!head.nonBlocking)
    return head
  return questionsOpen ? head : null
}

/**
 * The one rule for which requests count as waiting questions: a question is a
 * non-blocking request. The agent does not wait for it, so it waits for the
 * reader to open it.
 */
export function pendingQuestionCount(requests: readonly ControlRequest[]): number {
  return requests.filter(request => request.nonBlocking).length
}

interface ControlStoreState {
  pendingByAgent: Record<string, ControlRequest[]>
  questionsOpenByAgent: Record<string, boolean>
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
    questionsOpenByAgent: {},
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
    closeQuestionsWithoutOne(agentId)
    if (removed || claimToken) {
      const key = removalKey(agentId, requestId, claimToken, removed ? canonicalJSON(removed.payload) : '')
      if (removedInstances.get(key) !== ControlResponseState.COMPLETED)
        rememberRemoval(key, responseState)
    }
  }

  /**
   * Close the questions of one agent when none waits any more, so a later question
   * never opens by itself.
   */
  function closeQuestionsWithoutOne(agentId: string) {
    if (state.questionsOpenByAgent[agentId] && pendingQuestionCount(state.pendingByAgent[agentId] ?? []) === 0)
      setState('questionsOpenByAgent', agentId, false)
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
          // A merge can only set the flag, never clear it.
          if (request.nonBlocking && !existing.nonBlocking) {
            existing.nonBlocking = true
            // The entry leaves the blocking group behind: move it to the
            // non-blocking group at the end of the list.
            list.splice(list.indexOf(existing), 1)
            list.push(existing)
          }
          return
        }
        const entry = {
          ...request,
          ...(request.originalPayload !== undefined ? { originalPayload: request.originalPayload.slice() } : {}),
        }
        if (request.nonBlocking) {
          list.push(entry)
          return
        }
        // A blocking request joins the blocking group before the first
        // non-blocking entry, so every blocking request precedes every
        // non-blocking one and each group keeps its arrival order.
        const firstNonBlocking = list.findIndex(r => r.nonBlocking)
        if (firstNonBlocking < 0)
          list.push(entry)
        else
          list.splice(firstNonBlocking, 0, entry)
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
      // A delivered request can still need recording, and a non-blocking
      // question belongs to the conversation: both outlive their provider.
      setState('pendingByAgent', agentId, (state.pendingByAgent[agentId] ?? []).filter(request => request.responseState === ControlResponseState.DELIVERED || request.nonBlocking))
      closeQuestionsWithoutOne(agentId)
    },

    getRequests(agentId: string): ControlRequest[] {
      return state.pendingByAgent[agentId] ?? []
    },

    /** Whether the reader opened this agent's questions. */
    questionsOpen(agentId: string): boolean {
      return state.questionsOpenByAgent[agentId] ?? false
    },

    /**
     * Open or close this agent's questions. A call that opens them while no
     * question waits does nothing. Otherwise the flag would stay set, and the
     * next question would take over the composer when it arrives. A click on
     * the banner can race the removal of the last question, so the store
     * checks here.
     */
    setQuestionsOpen(agentId: string, open: boolean) {
      if (open && pendingQuestionCount(state.pendingByAgent[agentId] ?? []) === 0)
        return
      setState('questionsOpenByAgent', agentId, open)
    },

    /** The request the composer shows right now, if any. */
    shownRequest(agentId: string): ControlRequest | null {
      return shownControlRequest(state.pendingByAgent[agentId] ?? [], state.questionsOpenByAgent[agentId] ?? false)
    },

    clearAgent(agentId: string) {
      setState('pendingByAgent', agentId, [])
      closeQuestionsWithoutOne(agentId)
    },

    clearAll() {
      // `produce`, not `setState('pendingByAgent', {})`: a store write MERGES
      // the object it is given, so an empty one changes nothing at all (the
      // same hazard `DirectoryTree.tsx` documents). A draft assignment replaces
      // each map wholesale.
      setState(produce((s) => {
        s.pendingByAgent = {}
        s.questionsOpenByAgent = {}
      }))
    },
  }
}
