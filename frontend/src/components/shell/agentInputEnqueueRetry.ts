import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { blake2b } from '@noble/hashes/blake2.js'
import { bytesToHex } from '@noble/hashes/utils.js'
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

function payloadFingerprint(payload: AgentInputEnqueuePayload): string {
  const hash = blake2b.create({ dkLen: 32 })
  const encoder = new TextEncoder()
  const update = (data: Uint8Array) => {
    const size = new Uint8Array(8)
    new DataView(size.buffer).setBigUint64(0, BigInt(data.byteLength), true)
    hash.update(size)
    hash.update(data)
  }
  update(encoder.encode(payload.agentId))
  update(Uint8Array.of(payload.kind))
  update(encoder.encode(payload.text))
  update(encoder.encode(payload.scheduledAt))
  for (const attachment of payload.attachments) {
    update(encoder.encode(attachment.filename))
    update(encoder.encode(attachment.mimeType))
    update(attachment.data)
  }
  return bytesToHex(hash.digest())
}

/**
 * How many pending attempts one browser tab keeps.
 *
 * A failed attempt stays until the user sends the same payload again, and
 * nothing else removes it, so the map needs a cap: a fingerprint is small, but
 * the map would otherwise grow for the whole life of the tab. Insertion order
 * drives the eviction, oldest first.
 */
export const MAX_PENDING_ATTEMPTS = 20

interface PendingAttempt {
  inputId: string
  uncertain: boolean
}

/** Keep one client input ID until delivery has a known result. */
export function createAgentInputEnqueueRetry(mint: () => string = randomUUID) {
  // The complete payload identifies an attempt. Several abandoned drafts for
  // one agent must keep independent IDs when their responses are lost.
  const pendingByFingerprint = new Map<string, PendingAttempt>()
  const keyOf = (payload: AgentInputEnqueuePayload) => `${payload.agentId}\0${payload.kind}\0${payloadFingerprint(payload)}`

  const settle = (inputId: string, outcome: 'accepted' | 'refused' | 'uncertain') => {
    for (const [key, attempt] of pendingByFingerprint) {
      if (attempt.inputId !== inputId)
        continue
      if (outcome === 'uncertain') {
        attempt.uncertain = true
      }
      else if (outcome === 'accepted' || !attempt.uncertain) {
        // A later refusal cannot disprove an earlier unanswered delivery.
        // Keep that ID until a successful response settles its uncertainty.
        pendingByFingerprint.delete(key)
      }
      return
    }
  }

  return {
    inputIdFor(payload: AgentInputEnqueuePayload): string {
      const key = keyOf(payload)
      const attempt = pendingByFingerprint.get(key) ?? { inputId: mint(), uncertain: false }
      lruSet(pendingByFingerprint, key, attempt, MAX_PENDING_ATTEMPTS)
      return attempt.inputId
    },

    /** Acquire an unanswered attempt without minting an ID or hashing twice. */
    inputIdForPendingAttempt(payload: AgentInputEnqueuePayload): string | undefined {
      const key = keyOf(payload)
      const attempt = pendingByFingerprint.get(key)
      if (!attempt?.uncertain)
        return undefined
      lruSet(pendingByFingerprint, key, attempt, MAX_PENDING_ATTEMPTS)
      return attempt.inputId
    },

    markAccepted: (inputId: string) => settle(inputId, 'accepted'),
    markRefused: (inputId: string) => settle(inputId, 'refused'),
    markUncertain: (inputId: string) => settle(inputId, 'uncertain'),
  }
}
