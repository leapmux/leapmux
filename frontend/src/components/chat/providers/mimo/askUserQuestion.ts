/**
 * Which MiMo `question.asked` requests the question card answers.
 *
 * MiMo is a fork of OpenCode, and its question tool states the same questions and
 * takes the same answer. So the card reads and answers a MiMo question through the
 * shared OpenCode question wire (`../openCodeQuestions`), and only the choice of
 * which requests are questions is MiMo's own.
 *
 * Two `question.asked` requests are NOT questions for the card:
 *
 *   - A plan approval, whose question MiMo keys `plan_exit`. The worker records it
 *     under the plan tool's name, and the control surface reads it as a plan
 *     (`extractControl.ts`).
 *   - An MCP server's confirmation, keyed `mcp_elicitation`. It takes no free text,
 *     and the elicitation form answers it (`elicitation.ts`).
 */

import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { pickString } from '~/lib/jsonPick'
import { getToolName } from '~/utils/controlResponse'
import { mimoElicitation } from './elicitation'

/** True for a stored request that asks the reader questions. */
export function mimoIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === OPENCODE_EVENT.QuestionAsked
    && getToolName(payload) !== MIMO_TOOL.PlanExit
    && mimoElicitation(payload) === undefined
}
