import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'

/** A tool span belongs to one provider session within its LeapMux agent. */
export type MessageSpanIdentity = Pick<AgentChatMessage, 'spanId' | 'agentSessionId'>

export function messageSpanIdentity(message: MessageSpanIdentity): MessageSpanIdentity {
  return { spanId: message.spanId, agentSessionId: message.agentSessionId ?? '' }
}

export function messageSpanKey(identity: MessageSpanIdentity): string {
  return JSON.stringify([identity.agentSessionId ?? '', identity.spanId])
}

/**
 * The role of one message in its tool span.
 *
 * `request` supplies the tool request. `result` supplies its result.
 * `none` explicitly supplies neither side, such as a hidden progress record.
 * `other` leaves the role unknown, so the index uses its arrival-order fallback.
 * A known result keeps its side even when it arrives before the request.
 */
export type ToolSpanRole = 'request' | 'result' | 'none' | 'other'

/** The two sides that a span can pair. */
export type ToolSpanSide = Extract<ToolSpanRole, 'request' | 'result'>

/**
 * The revision of one message.
 * Its ID and sequence identify the row. Two counters can change while that identity remains the same.
 * The content version tracks an in-place body change. The supplemental revision tracks a later supplement.
 * One span holds a revision for each side. A row cache uses the exact revisions that the row reads.
 */
export interface MessageRevision {
  id: string
  seq: bigint
  contentVersion: number
  supplementalRevision: bigint
}
