import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { digestFieldsOffThread } from '~/lib/fieldDigestClient'
import { randomUUID } from '~/lib/idGenerator'
import { lruSet } from '~/lib/mapLru'

export interface AgentInputEnqueuePayload {
  agentId: string
  kind: AgentInputKind
  text: string
  attachments: readonly FileAttachment[]
  /**
   * The wire schedule (`ISO` UTC), or the empty string for an immediate send.
   * Part of the fingerprint on purpose: a resend that changes ONLY the
   * schedule must mint a fresh input id, because the Worker refuses to reuse
   * one queue id whose accepted content differs.
   */
  scheduledAt: string
}

/**
 * Hash every part of the payload, attachment bytes included, so that a change to any
 * byte mints a fresh input ID.
 *
 * A payload carries up to 10 MiB per item, and hashing 10 MiB on the main thread
 * takes about 190 ms. The hash runs in a worker instead (see digestFieldsOffThread),
 * and the main thread pays only for the copy of the bytes.
 */
function payloadFingerprint(payload: AgentInputEnqueuePayload): Promise<string> {
  const encoder = new TextEncoder()
  const fields: Uint8Array[] = [
    encoder.encode(payload.agentId),
    Uint8Array.of(payload.kind),
    encoder.encode(payload.text),
    encoder.encode(payload.scheduledAt),
  ]
  for (const attachment of payload.attachments)
    fields.push(encoder.encode(attachment.filename), encoder.encode(attachment.mimeType), attachment.data)
  return digestFieldsOffThread(fields)
}

/**
 * How many pending attempts one browser tab keeps.
 *
 * An unanswered attempt can stay indefinitely if the user abandons the draft.
 * Cap the map and evict the least recently used attempt.
 */
export const MAX_PENDING_ATTEMPTS = 20

export interface AgentInputEnqueueAttempt {
  readonly agentId: string
  readonly inputId: string
  state: 'pending' | 'uncertain' | 'accepted'
}

/** Keep one client input ID until delivery has a known result. */
export function createAgentInputEnqueueRetry(mint: () => string = randomUUID) {
  // The complete payload identifies an attempt. Several abandoned drafts for
  // one agent must keep independent IDs when their responses are lost.
  const pendingByFingerprint = new Map<string, AgentInputEnqueueAttempt>()

  /**
   * Hash the payload, find its attempt, and return what `decide` makes of it.
   *
   * The hash runs off the main thread, and a response can settle the attempt in
   * between. A call that started while the attempt was pending still finds it,
   * as it did when the hash ran in one task: a fresh ID would deliver the payload
   * a second time. `pending` is false for such a settled attempt, which must stay
   * out of the map.
   *
   * `decide` runs in the same task as the lookup. A second call for the same
   * payload that finishes its hash in the meantime would otherwise find no
   * attempt either, and both calls would mint an ID.
   */
  const withAttempt = async <T>(
    payload: AgentInputEnqueuePayload,
    decide: (fingerprint: string, attempt: AgentInputEnqueueAttempt | undefined, pending: boolean) => T,
  ): Promise<T> => {
    const pendingAtCall = new Map(pendingByFingerprint)
    const fingerprint = await payloadFingerprint(payload)
    const live = pendingByFingerprint.get(fingerprint)
    // A new send may have minted another attempt after the one this call saw
    // was accepted. This call still belongs to its earlier attempt.
    const attempt = pendingAtCall.get(fingerprint) ?? live
    return decide(fingerprint, attempt, attempt !== undefined && attempt === live)
  }

  const settle = (attempt: AgentInputEnqueueAttempt, outcome: 'accepted' | 'refused' | 'uncertain') => {
    if (outcome === 'uncertain') {
      if (attempt.state !== 'accepted')
        attempt.state = 'uncertain'
      return
    }
    // A later refusal cannot disprove an earlier unanswered delivery.
    if (outcome === 'refused' && attempt.state === 'uncertain')
      return
    if (outcome === 'accepted')
      attempt.state = 'accepted'
    for (const [key, pending] of pendingByFingerprint) {
      if (pending === attempt) {
        pendingByFingerprint.delete(key)
        return
      }
    }
  }

  return {
    attemptFor: (payload: AgentInputEnqueuePayload): Promise<AgentInputEnqueueAttempt> => withAttempt(payload, (fingerprint, attempt, pending) => {
      if (attempt === undefined) {
        const minted: AgentInputEnqueueAttempt = {
          agentId: payload.agentId,
          inputId: mint(),
          state: 'pending',
        }
        lruSet(pendingByFingerprint, fingerprint, minted, MAX_PENDING_ATTEMPTS)
        return minted
      }
      if (pending)
        lruSet(pendingByFingerprint, fingerprint, attempt, MAX_PENDING_ATTEMPTS)
      return attempt
    }),

    /** Acquire an unanswered attempt without minting an ID or hashing twice. */
    pendingAttemptFor: (payload: AgentInputEnqueuePayload): Promise<AgentInputEnqueueAttempt | undefined> => withAttempt(payload, (fingerprint, attempt, pending) => {
      if (attempt === undefined || attempt.state === 'pending')
        return undefined
      if (pending)
        lruSet(pendingByFingerprint, fingerprint, attempt, MAX_PENDING_ATTEMPTS)
      return attempt
    }),

    /**
     * A queue item or a stored transcript row proves acceptance. A later
     * send of the same payload takes a fresh ID, even if the original input
     * has already left the queue.
     */
    observeAccepted: (agentId: string, inputId: string) => {
      for (const [key, attempt] of pendingByFingerprint) {
        if (attempt.agentId === agentId && attempt.inputId === inputId) {
          attempt.state = 'accepted'
          pendingByFingerprint.delete(key)
          return
        }
      }
    },

    markAccepted: (attempt: AgentInputEnqueueAttempt) => settle(attempt, 'accepted'),
    markRefused: (attempt: AgentInputEnqueueAttempt) => settle(attempt, 'refused'),
    markUncertain: (attempt: AgentInputEnqueueAttempt) => settle(attempt, 'uncertain'),
  }
}
