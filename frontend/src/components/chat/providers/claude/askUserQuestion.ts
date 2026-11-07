import type { ControlQuestion } from '../../model/question'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { questionsFromWire } from '../../controls/types'
import { CLAUDE_TOOL_NAMES } from './toolNames'

/**
 * Whether one control payload is Claude's question prompt.
 *
 * ONE predicate, read by the plugin's `askUserQuestion` hook (which answers it) and
 * by `extractControl` (which draws it). A second copy would let the banner draw a
 * question the composer refused to send.
 */
export function claudeIsAskUserQuestion(payload: Record<string, unknown>): boolean {
  const tool = getToolName(payload)
  return tool === CLAUDE_TOOL_NAMES.ASK_USER_QUESTION
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
