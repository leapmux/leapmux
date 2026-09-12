/**
 * ZCode `interaction/requestUserInput` -> AskUserQuestion adapter.
 *
 * The worker already stores the questions in the shared control's own shape, under
 * `request.input.questions`, so the shared `AskUserQuestionContent` reads them with
 * no adapter at all. This module exists for the two things it cannot do:
 *
 *   - Normalize a question the app-server sent with an empty label or an empty
 *     value, so an option always has something to click.
 *   - Give the plugin ONE reader that both its registry hook and its control
 *     components call, so the two surfaces cannot disagree about what is on screen.
 */

import type { Question } from '../../controls/types'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput } from '~/utils/controlResponse'

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
 * Build the `Question[]` for a stored ZCode user-input control request.
 *
 * Returns an empty array for a request that declares no question -- a plan approval
 * reaches the plan surface instead, which needs none.
 */
export function zcodeQuestionsFromPayload(payload: Record<string, unknown>): Question[] {
  const params = pickObject(payload, 'params')
  const candidates = [pickObject(params, 'schema')?.questions, params?.questions, pickObject(params, 'input')?.questions]
  // The native request retains descriptions that the worker's compact header omits.
  const questions = candidates.find(value => Array.isArray(value) && value.length)
    ?? candidates.find(Array.isArray)
    ?? getToolInput(payload).questions
  if (!Array.isArray(questions))
    return []
  return questions.flatMap((raw) => {
    if (!isObject(raw))
      return []
    const question = pickString(raw, 'question')
    const header = pickString(raw, 'header')
    // The answer is keyed by the question TEXT, so a question with neither text nor
    // header could never be answered in a way the app-server matches.
    if (!question && !header)
      return []
    const built: Question = {
      question: question || header,
      options: zcodeOptions(raw),
    }
    if (header)
      built.header = header
    if (raw.multiSelect === true)
      built.multiSelect = true
    return [built]
  })
}
