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
 * The role one message plays in its tool span.
 *
 * `request` is the side that asks -- the tool_use, the start event, the item
 * still running. The word is REQUEST rather than the arrival-order "opener"
 * because routing is by ROLE: a result that arrives first still files as the
 * result, and "opener" stated an order the index never used.
 */
export type ToolSpanRole = 'request' | 'result' | 'other'

/** The two sides a span PAIRS: the request that asks and the result that answers. */
export type ToolSpanSide = Exclude<ToolSpanRole, 'other'>

/**
 * The revision of ONE message: its identity, its sequence, and the two counters
 * that can move while the identity stays put -- the store's in-place content
 * version and the supplemental revision a late supplement bumps.
 *
 * Named for the MESSAGE rather than the span: one span holds two of these, one
 * per side, and a row's caches key on the exact set it depends on.
 */
export interface MessageRevision {
  id: string
  seq: bigint
  contentVersion: number
  supplementalRevision: bigint
}
