import type { ControlQuestion } from '../../model/question'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { questionsFromWire } from '../../controls/types'

/**
 * Qoder's question tool name.
 *
 * The tool states one to four questions with two to four options each, an
 * optional short header and a multi-select flag. Its reply is the same tool
 * input with an `answers` record added: the CLI accepts an answer keyed by the
 * question text or by its index, and normalizes the keys itself.
 */
const QODER_ASK_USER_QUESTION = 'AskUserQuestion'

/**
 * Whether one control payload is Qoder's question prompt.
 *
 * The one recognizer of a question for this provider: the shared control
 * surface asks it before `extractControl` sees the request, so the answer and
 * the banner cannot disagree about what one payload is.
 */
export function qoderIsAskUserQuestion(payload: Record<string, unknown>): boolean {
  return getToolName(payload) === QODER_ASK_USER_QUESTION
}

/**
 * The questions one such payload asks, in the order the tool declared them.
 *
 * The shared reader, not a cast: an `Array.isArray` on the OUTER array says
 * nothing about the elements, and a `null` or a bare string among them would
 * reach the control, which dereferences `question` and hands `options` to a
 * `<For>`.
 */
export function qoderAskUserQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  return questionsFromWire(getToolInput(payload).questions).map((question) => {
    // Only a real `true` opens the multiple-choice control: the field is a
    // boolean on the wire, and anything else a model writes is not one.
    const { multiSelect, ...rest } = question
    return multiSelect === true ? { ...rest, multiSelect: true } : rest
  })
}
