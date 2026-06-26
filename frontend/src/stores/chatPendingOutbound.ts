import { createPerAgentStore } from './chatPerAgentStore'

// ---------------------------------------------------------------------------
// Pending-outbound queue slice
//
// Per-agent queue of messages composed while the agent subprocess is still
// starting (AgentStatus.STARTING). Drained when the status transitions to
// ACTIVE; cleared (and per-message errors set by the caller) on STARTUP_FAILED.
// Independent of the windowing invariants.
// ---------------------------------------------------------------------------

/** Plain attachment shape passed to workerRpc.sendAgentMessage as MessageInit. */
export interface PendingOutboundAttachment {
  filename: string
  mimeType: string
  data: Uint8Array
}

export interface PendingOutboundMessage {
  localId: string
  content: string
  attachments: PendingOutboundAttachment[]
}

export function createPendingOutboundStore() {
  const base = createPerAgentStore<PendingOutboundMessage[]>([])
  return {
    enqueue(agentId: string, msg: PendingOutboundMessage) {
      base.set(agentId, [...base.get(agentId), msg])
    },
    /** Remove and return the queued messages for an agent ([] when none). */
    take(agentId: string): PendingOutboundMessage[] {
      const existing = base.get(agentId)
      if (existing.length === 0)
        return []
      base.clear(agentId)
      return existing
    },
    /** Drop the agent's pending-outbound queue entirely (agent close). */
    remove: base.remove,
  }
}
