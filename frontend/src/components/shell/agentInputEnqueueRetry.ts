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

interface EnqueueAttempt {
  inputId: string
  fingerprint: string
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

/** Keep one client input ID until the matching enqueue succeeds. */
export function createAgentInputEnqueueRetry(mint: () => string = randomUUID) {
  const pendingByKind = new Map<string, EnqueueAttempt>()
  return {
    inputIdFor(payload: AgentInputEnqueuePayload): string {
      const key = `${payload.agentId}\0${payload.kind}`
      const pending = pendingByKind.get(key)
      const fingerprint = payloadFingerprint(payload)
      if (pending?.fingerprint === fingerprint)
        return pending.inputId
      const inputId = mint()
      pendingByKind.set(key, { inputId, fingerprint })
      return inputId
    },

    markAccepted(inputId: string): void {
      for (const [key, pending] of pendingByKind) {
        if (pending.inputId === inputId) {
          pendingByKind.delete(key)
          return
        }
      }
    },
  }
}
