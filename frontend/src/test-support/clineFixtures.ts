import { CLINE_CAPABILITY, CLINE_EVENT } from '~/generated/contracts/cline-protocol'

/**
 * Cline's persisted control requests, for the plugin's tests and the cross-provider
 * parity tests.
 *
 * The worker publishes Cline's own hub event envelope as the control request, so a
 * request is that envelope: the event name, the session, and the event's payload. Every
 * test that feeds Cline its own wire shape builds it here, so one change to that shape
 * reaches all of them. The shapes are the 3.0.64 daemon's own, captured from probes.
 */

/** One Cline event envelope. */
export function clineEvent(event: string, payload: Record<string, unknown>): Record<string, unknown> {
  return { version: 'v1', event, sessionId: 'session_1', payload }
}

/** One pending tool approval, as the control surface receives it. */
export function clineApprovalRequest(toolName: string, input: Record<string, unknown>, fields: Record<string, unknown> = {}): Record<string, unknown> {
  return clineEvent(CLINE_EVENT.ApprovalRequested, {
    approvalId: 'approval_1',
    sessionId: 'session_1',
    agentId: 'agent_1',
    conversationId: 'conv_1',
    iteration: 1,
    toolCallId: 'call_1',
    toolName,
    inputJson: JSON.stringify(input),
    policy: { autoApprove: false },
    ...fields,
  })
}

/** One pending question, as the control surface receives it. */
export function clineQuestionRequest(question: string, options: string[]): Record<string, unknown> {
  return clineEvent(CLINE_EVENT.CapabilityRequested, {
    requestId: 'capreq_1',
    targetClientId: 'leapmux-agent',
    capabilityName: CLINE_CAPABILITY.AskQuestion,
    payload: { executor: 'askQuestion', args: [question, options], context: { sessionId: 'session_1', agentId: 'agent_1', toolCallId: 'call_q' } },
  })
}
