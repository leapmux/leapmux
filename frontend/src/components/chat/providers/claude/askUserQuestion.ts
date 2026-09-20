import type { ControlQuestion } from '../../model/question'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { questionsFromWire } from '../../controls/types'
import { CLAUDE_TOOL_NAMES } from './toolNames'

/**
 * The older spelling of the question tool.
 *
 * Not in `CLAUDE_TOOL_NAMES`, which holds the names a TRANSCRIPT row can carry. This one
 * reaches the control channel alone, from a release that predates the rename, and a
 * saved request still carries it.
 */
const CLAUDE_REQUEST_USER_INPUT = 'request_user_input'

/**
 * Whether one control payload is Claude's question prompt.
 *
 * ONE predicate, read by the plugin's `askUserQuestion` hook (which answers it) and
 * by `extractControl` (which draws it). A second copy would let the banner draw a
 * question the composer refused to send.
 */
export function claudeIsAskUserQuestion(payload: Record<string, unknown>): boolean {
  const tool = getToolName(payload)
  return tool === CLAUDE_TOOL_NAMES.ASK_USER_QUESTION || tool === CLAUDE_REQUEST_USER_INPUT
}

/**
 * The questions one such payload asks, in the order the tool declared them.
 *
 * The shared reader, not a cast: an `Array.isArray` on the OUTER array says nothing
 * about the elements, and a `null` or a bare string among them reached
 * `AskUserQuestionControl`, which dereferences `question` and hands `options` to a
 * `<For>`.
 */
export function claudeAskUserQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  return questionsFromWire(getToolInput(payload).questions)
}
