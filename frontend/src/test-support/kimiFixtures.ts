import { KIMI_EVENT, KIMI_FRAME, KIMI_INTERACTION_KIND } from '~/generated/contracts/kimi-protocol'

/**
 * Build the event payloads that Kimi's kap-server sends.
 * The Worker stores these payloads unchanged.
 * The outer frame carries session_id separately from these tool payloads.
 * Native control interactions use snake-case identity fields and a nested request.
 */

/** One event row of the main agent, or of the given agent. */
export function kimiFrame(type: string, fields: Record<string, unknown> = {}, agentId = 'main'): Record<string, unknown> {
  return { type, agentId, ...fields }
}

/** The `tool.call.started` row that opens one tool call. */
export function kimiToolStart(
  toolCallId: string,
  name: string,
  args: Record<string, unknown>,
  display?: Record<string, unknown>,
  agentId = 'main',
): Record<string, unknown> {
  return kimiFrame(KIMI_EVENT.ToolCallStarted, {
    turnId: 0,
    toolCallId,
    name,
    args,
    ...(display
      ? { display }
      : {}),
  }, agentId)
}

/** The `tool.result` row that closes one tool call. */
export function kimiToolResult(toolCallId: string, output: unknown, extra: Record<string, unknown> = {}, agentId = 'main'): Record<string, unknown> {
  return kimiFrame(KIMI_EVENT.ToolResult, {
    turnId: 0,
    toolCallId,
    output,
    ...extra,
  }, agentId)
}

type KimiControlFixture = Record<string, unknown> & { request: Record<string, unknown> }

/** One pending approval, as the control surface receives it. */
export function kimiApprovalRequest(toolName: string, display: Record<string, unknown>, fields: Record<string, unknown> = {}): KimiControlFixture {
  const { agent_id = 'main', ...requestFields } = fields
  return {
    type: KIMI_FRAME.Interaction,
    kind: KIMI_INTERACTION_KIND.Approval,
    status: 'pending',
    interaction_id: 'approval_1',
    session_id: 'session_1',
    agent_id,
    tool_call_id: 'call_1',
    request: {
      tool_name: toolName,
      action: `Running: ${toolName}`,
      tool_input_display: display,
      ...requestFields,
    },
  }
}

/** One pending question request, as the control surface receives it. */
export function kimiQuestionRequest(questions: Record<string, unknown>[]): KimiControlFixture {
  return {
    type: KIMI_FRAME.Interaction,
    kind: KIMI_INTERACTION_KIND.Question,
    status: 'pending',
    interaction_id: 'question_1',
    session_id: 'session_1',
    agent_id: 'main',
    tool_call_id: 'call_ask',
    request: { questions },
  }
}
