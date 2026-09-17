/**
 * ZCode `interaction/requestUserInput` -> AskUserQuestion adapter.
 *
 * The worker already stores the questions in the shared control's own shape, under
 * `request.input.questions`, so the shared `AskUserQuestionContent` reads them with
 * no adapter at all. This module exists for the three things it cannot do:
 *
 *   - Normalize a question the app-server sent with an empty label or an empty
 *     value, so an option always has something to click.
 *   - Give the plugin ONE reader that both its registry hook and its control
 *     components call, so the two surfaces cannot disagree about what is on screen.
 *   - Give the saved-answer display (`zcodeControlResponseDisplay`) the SAME question
 *     list, in the same order, that the reader answered.
 */

import type { Question } from '../../controls/types'
import type { QuestionIR } from '../../ir/questionBody'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { questionsFromRecords } from '../questionRecords'

/**
 * The options of one question, with the value/label pair repaired.
 *
 * ZCode's wire form sets an option's `value` to its own LABEL, and the shared
 * control keys the answer by the label it shows -- so a blank label would send a
 * blank answer, which the app-server discards. Either field standing in for the
 * other keeps the option answerable; an option with neither is dropped, because it
 * has nothing to send.
 */
function zcodeOptions(question: Record<string, unknown>): Question['options'] {
  const options = question.options
  if (!Array.isArray(options))
    return []
  return options.flatMap((option) => {
    if (!isObject(option))
      return []
    const label = pickString(option, 'label') || pickString(option, 'value')
    if (!label)
      return []
    const description = pickString(option, 'description')
    const preview = pickString(option, 'preview')
    return [{ label, ...(description ? { description } : {}), ...(preview.trim() ? { preview } : {}) }]
  })
}

/**
 * The raw question records of a stored ZCode user-input control request, in the order
 * that the request declares.
 *
 * This is the ONE question list of the provider. The control surface answers this list,
 * and `zcodeControlResponseDisplay` reads the saved answer back through it. A second
 * list lets the two surfaces disagree about which question one answer belongs to, and
 * the positional `answer_<index>` fallback then shows an answer under the wrong
 * question.
 *
 * The precedence puts the native request (`params`) first, because it retains the
 * descriptions that the worker's compact header omits. An EMPTY native list stays a
 * real answer: it does not fall back to a populated one, because the app-server that
 * sent it declares no question. The worker's compact header
 * (`request.input.questions`) is the last source, for a request that carries no native
 * params at all.
 */
export function zcodeQuestionRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  const params = pickObject(payload, 'params')
  const candidates = [pickObject(params, 'schema')?.questions, params?.questions, pickObject(params, 'input')?.questions]
  const questions = candidates.find(value => Array.isArray(value) && value.length)
    ?? candidates.find(Array.isArray)
    ?? getToolInput(payload).questions
  if (!Array.isArray(questions))
    return []
  return questions.filter(isObject)
}

/**
 * The text that keys one question's answer: the question itself, or the header when the
 * app-server sent a header alone.
 *
 * The shared control shows this text and keys the answer map by it, so the saved-answer
 * display must look the answer up under the same text. An empty result marks a record
 * that nothing can answer.
 */
export function zcodeQuestionText(question: Record<string, unknown>): string {
  return pickString(question, 'question') || pickString(question, 'header')
}

/**
 * The questions of an `AskUserQuestion` TOOL CALL, for the row that draws it.
 *
 * The control surface reads the stored control REQUEST, which carries the same
 * questions under `params`. This reads the tool call's own input instead, because a
 * transcript row is built from the tool frame and the control request is a separate
 * message that a replay may not have beside it.
 *
 * ZCode's option sets `value` to the label, so either field standing in for the
 * other keeps the option readable -- the same repair `zcodeOptions` makes for the
 * answerable list.
 */
export function zcodeQuestionsFromToolInput(input: Record<string, unknown>): QuestionIR[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return { ...(header ? { header } : {}), question: zcodeQuestionText(question) }
    },
    (option) => {
      const label = pickString(option, 'label') || pickString(option, 'value')
      if (!label)
        return null
      const description = pickString(option, 'description')
      const preview = pickString(option, 'preview')
      return {
        label,
        ...(description ? { description } : {}),
        ...(preview ? { preview } : {}),
      }
    },
  )
}

/**
 * Build the `Question[]` for a stored ZCode user-input control request.
 *
 * Returns an empty array for a request that declares no question -- a plan approval
 * reaches the plan surface instead, which needs none.
 */
export function zcodeQuestionsFromPayload(payload: Record<string, unknown>): Question[] {
  return zcodeQuestionRecords(payload).flatMap((raw) => {
    const text = zcodeQuestionText(raw)
    // The answer is keyed by the question TEXT, so a question with neither text nor
    // header could never be answered in a way the app-server matches.
    if (!text)
      return []
    const header = pickString(raw, 'header')
    const built: Question = {
      question: text,
      options: zcodeOptions(raw),
    }
    if (header)
      built.header = header
    if (raw.multiSelect === true)
      built.multiSelect = true
    return [built]
  })
}

/**
 * Whether a stored ZCode control payload is the AskUserQuestion prompt.
 *
 * ZCode multiplexes three prompts over two RPCs, and the worker records which one
 * arrived as the request's TOOL NAME. `zcodeExtractControl` switches on the same
 * name; this one answers the shared question capability, which the composer reads
 * to pick its editor.
 */
export function zcodeIsAskUserQuestion(payload: Record<string, unknown>): boolean {
  return getToolName(payload) === ZCODE_TOOL.AskUserQuestion
}
