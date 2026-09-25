import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'

/**
 * Kimi Code's persisted rows, for the plugin's tests and the cross-provider parity
 * tests.
 *
 * The worker persists every kap-server event VERBATIM, so a row is the event payload
 * itself: its `type`, its `agentId`, and the fields of that event. Every test that
 * feeds Kimi Code its own wire shape builds it here, so one change to that shape
 * reaches all of them. The shapes are the 2.0.2 server's own, captured from live sessions.
 */

/** One event row of the main agent, or of the given agent. */
export function kimiFrame(type: string, fields: Record<string, unknown> = {}, agentId = 'main'): Record<string, unknown> {
  return { type, agentId, sessionId: 'session_1', ...fields }
}

/** The `tool.call.started` row that opens one tool call. */
export function kimiToolStart(
  toolCallId: string,
  name: string,
  args: Record<string, unknown>,
  display?: Record<string, unknown>,
  agentId = 'main',
): Record<string, unknown> {
  return kimiFrame(KIMI_EVENT.ToolCallStarted, { turnId: 0, toolCallId, name, args, ...(display ? { display } : {}) }, agentId)
}

/** The `tool.result` row that closes one tool call. */
export function kimiToolResult(toolCallId: string, output: unknown, extra: Record<string, unknown> = {}, agentId = 'main'): Record<string, unknown> {
  return kimiFrame(KIMI_EVENT.ToolResult, { turnId: 0, toolCallId, output, ...extra }, agentId)
}

/** One pending approval, as the control surface receives it. */
export function kimiApprovalRequest(toolName: string, display: Record<string, unknown>, fields: Record<string, unknown> = {}): Record<string, unknown> {
  return kimiFrame(KIMI_EVENT.ApprovalRequested, {
    approval_id: 'approval_1',
    session_id: 'session_1',
    agent_id: 'main',
    turn_id: 0,
    tool_call_id: 'call_1',
    tool_name: toolName,
    action: `Running: ${toolName}`,
    tool_input_display: display,
    ...fields,
  })
}

/** One pending question request, as the control surface receives it. */
export function kimiQuestionRequest(questions: Record<string, unknown>[]): Record<string, unknown> {
  return kimiFrame(KIMI_EVENT.QuestionRequested, {
    question_id: 'question_1',
    session_id: 'session_1',
    agent_id: 'main',
    turn_id: 0,
    tool_call_id: 'call_ask',
    questions,
  })
}
