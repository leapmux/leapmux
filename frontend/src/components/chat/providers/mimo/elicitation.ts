import type { ElicitationRequest } from '../../model/controlPrompt'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { MIMO_QUESTION_KEY } from './protocol'

/**
 * An MCP server's confirmation, which MiMo asks as a question.
 *
 * MiMo HEAD (`mcp/elicitation.ts`; no release has it yet) refuses every elicitation
 * that asks for a value, and asks the rest as ONE question keyed `mcp_elicitation`:
 * the server as the header, the server, the message and a subtitle joined by blank
 * lines as the question, the three answers Accept, Decline and Cancel, and
 * `custom: false`. MiMo reads Accept and Decline as those MCP actions and any other
 * answer as Cancel.
 *
 * The question card would offer free text and YOLO, which MiMo reads as Cancel, so
 * the shared elicitation form draws the request instead: an empty confirmation form
 * with Approve, Reject and Cancel. The worker sends the chosen action back as MiMo's
 * own answer.
 *
 * Undefined for any other request.
 */
export function mimoElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (pickString(payload, 'type') !== OPENCODE_EVENT.QuestionAsked)
    return undefined
  const questions = pickObject(payload, 'properties')?.questions
  if (!Array.isArray(questions) || questions.length !== 1)
    return undefined
  const question = questions[0]
  if (!isObject(question) || pickString(question, 'key') !== MIMO_QUESTION_KEY.McpElicitation)
    return undefined
  const server = pickString(question, 'header')
  const text = pickString(question, 'question')
  // MiMo puts the server in front of the message, and the form states the server on
  // its own line already.
  const prefix = `${server}\n\n`
  const message = server && text.startsWith(prefix) ? text.slice(prefix.length) : text
  return {
    mode: 'form',
    ...(server ? { server } : {}),
    message,
    schema: { type: 'object', properties: {} },
  }
}
