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
import { isObject, pickString } from '~/lib/jsonPick'
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
function zcodeOptions(question: Record<string, unknown>): Array<{ label: string, description?: string }> {
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
    return [description ? { label, description } : { label }]
  })
}

/**
 * Build the `Question[]` for a stored ZCode user-input control request.
 *
 * Returns an empty array for a request that declares no question -- a plan approval
 * reaches the plan surface instead, which needs none.
 */
export function zcodeQuestionsFromPayload(payload: Record<string, unknown>): Question[] {
  const questions = getToolInput(payload).questions
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

/**
 * The plan text of a stored plan-approval request.
 *
 * `plan` is where the worker puts it, and it is read FIRST. A plan approval carries no
 * question of its own, so the worker synthesizes one — and that question's text is
 * fixed boilerplate ("Review this implementation plan."). Reading the question first
 * would render that boilerplate as the plan and the real plan would never appear.
 *
 * The question text and then `prompt` remain as fallbacks, for a build that states the
 * plan in one of them and sends no `plan`.
 */
export function zcodePlanText(payload: Record<string, unknown>): string {
  const input = getToolInput(payload)
  const plan = pickString(input, 'plan')
  if (plan)
    return plan
  const questions = input.questions
  if (Array.isArray(questions)) {
    for (const raw of questions) {
      const question = isObject(raw) ? pickString(raw, 'question') : ''
      if (question)
        return question
    }
  }
  return pickString(input, 'prompt')
}
