import { CODEWHALE_EVENT, CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleEvent } from './toolResults.fixtures'

// Stored control requests in the shape the worker publishes them: the shared
// `request` header beside the runtime's own event (`buildControlPayload` in
// backend/internal/worker/agent/providers/codewhale/control.go).

export const QUESTIONS = [
  { header: 'Color', id: 'color', question: 'Which color?', options: [{ label: 'Red', description: 'Warm' }, { label: 'Blue', description: 'Cool' }], allow_free_text: true, multi_select: false },
  { header: 'Sizes', id: 'sizes', question: 'Which sizes?', options: [{ label: 'S' }, { label: 'M' }], multi_select: true },
]

/** A stored approval of one tool call. */
export function approvalPayload(toolName: string, input: Record<string, unknown>, fields: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    type: 'control_request',
    request_id: 'approval:ap1',
    request: { tool_name: toolName, tool_use_id: 'call-1', input },
    event: codewhaleEvent(CODEWHALE_EVENT.ApprovalRequired, { id: 'ap1', approval_id: 'ap1', tool_call_id: 'call-1', tool_name: toolName, description: 'The tool\'s static description.', intent_summary: null, ...fields }),
  }
}

/** A stored question. */
export function questionPayload(questions: unknown[] = QUESTIONS): Record<string, unknown> {
  const request = { questions }
  return {
    type: 'control_request',
    request_id: 'user_input:q1',
    request: { tool_name: CODEWHALE_TOOL.RequestUserInput, tool_use_id: 'q1', input: request },
    event: codewhaleEvent(CODEWHALE_EVENT.UserInputRequired, { id: 'q1', request }),
  }
}
