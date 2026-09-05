import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentInputKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { blake2b } from '@noble/hashes/blake2.js'
import { bytesToHex } from '@noble/hashes/utils.js'
import { randomUUID } from '~/lib/idGenerator'

export interface AgentInputEnqueuePayload {
  agentId: string
  kind: AgentInputKind
  text: string
  attachments: readonly FileAttachment[]
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

/** Keep one client input ID until the matching enqueue succeeds. */
export function createAgentInputEnqueueRetry(mint: () => string = randomUUID) {
  // Keyed on the WHOLE payload, not on (agent, kind). A failed send leaves the
  // text in the composer, and the user can abandon it and type a different
  // message. Two pending attempts for one agent must coexist, so a later
  // re-send of either recovers its own input ID. One ID for each (agent, kind)
  // evicted the first attempt, and a re-send of it then minted a fresh ID --
  // which duplicates the input when the first enqueue reached the Worker after
  // all and only the answer was lost.
  const pendingByFingerprint = new Map<string, string>()

  function remember(key: string, inputId: string): void {
    // delete-then-set moves the entry to the most recent insertion slot, so a
    // repeated attempt for one payload does not age out early.
    pendingByFingerprint.delete(key)
    pendingByFingerprint.set(key, inputId)
    while (pendingByFingerprint.size > MAX_PENDING_ATTEMPTS) {
      const oldest = pendingByFingerprint.keys().next().value
      if (oldest === undefined)
        break
      pendingByFingerprint.delete(oldest)
    }
  }

  return {
    inputIdFor(payload: AgentInputEnqueuePayload): string {
      const key = `${payload.agentId}\0${payload.kind}\0${payloadFingerprint(payload)}`
      const pending = pendingByFingerprint.get(key)
      const inputId = pending ?? mint()
      remember(key, inputId)
      return inputId
    },

    markAccepted(inputId: string): void {
      for (const [key, pendingInputId] of pendingByFingerprint) {
        if (pendingInputId === inputId) {
          pendingByFingerprint.delete(key)
          return
        }
      }
    },
  }
}
