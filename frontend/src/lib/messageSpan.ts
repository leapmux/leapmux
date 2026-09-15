import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'

/** A tool span belongs to one provider session within its LeapMux agent. */
export type MessageSpanIdentity = Pick<AgentChatMessage, 'spanId' | 'agentSessionId'>

export function messageSpanIdentity(message: MessageSpanIdentity): MessageSpanIdentity {
  return { spanId: message.spanId, agentSessionId: message.agentSessionId ?? '' }
}

export function messageSpanKey(identity: MessageSpanIdentity): string {
  return JSON.stringify([identity.agentSessionId ?? '', identity.spanId])
}
