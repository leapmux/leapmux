import type { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'

/** The browser reports this receipt after it handles an authoritative settled edge. */
export const AGENT_SETTLED_EVENT = 'leapmux:agent-settled'

export interface AgentSettledEventDetail {
  agentId: string
  state: AgentActivityState
  numToolUses?: number
}
