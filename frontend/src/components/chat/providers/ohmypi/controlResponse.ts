import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { OH_MY_PI_ASK_ANSWER, OH_MY_PI_ASK_ENVELOPE, OH_MY_PI_ASK_TYPE, OH_MY_PI_DIALOG_RESPONSE } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { isAllowPermissionKind, isRejectPermissionKind } from '../../model/controlPrompt'
import { CONTROL_DECISION_WORDS, label } from '../../persistedControlResponse'

/*
 * The display of a saved omp answer.
 *
 * omp's dialogs take `extension_ui_response` lines on its stdin, and the saved row keeps
 * the line that the Worker sent:
 *
 *   select / input / editor -> {type, id, value}
 *   confirm                 -> {type, id, confirmed}
 *   any dismissal           -> {type, id, cancelled: true}
 *
 * The question bridge takes LeapMux's own answer envelope instead
 * (`leapmux_ask_answer`), which the worker turns into the dialog chain omp waits on.
 */

/**
 * The bridge's saved answer, one `question: answer` line each, headed by the question
 * of the recorded prompt that the answer's id identifies.
 *
 * The answer selects each option by its value, which the bridge chooses and the reader
 * never sees, so each value reads as the label of the recorded option that it selects.
 * A value that no recorded option states reads as it stands.
 *
 * The bridge states every question of the call, the ones the reader left blank
 * included. A blank question has no line, so a heading never stands with no answer
 * after it.
 */
function askAnswerLines(response: Record<string, unknown>, questions: readonly ControlQuestion[]): string {
  const answers = Array.isArray(response[OH_MY_PI_ASK_ENVELOPE.Answers])
    ? (response[OH_MY_PI_ASK_ENVELOPE.Answers] as unknown[]).filter(isObject)
    : []
  return answers.flatMap((answer) => {
    const id = pickString(answer, OH_MY_PI_ASK_ANSWER.ID)
    const question = id
      ? questions.find(candidate => candidate.id === id)
      : undefined
    const labels = new Map(question?.options.map(option => [option.value ?? option.label, option.label]))
    const text = [
      stringArray(answer[OH_MY_PI_ASK_ANSWER.Selected]).map(value => labels.get(value) ?? value).join(', '),
      pickString(answer, OH_MY_PI_ASK_ANSWER.Custom),
    ]
      .filter(part => part.trim() !== '')
      .join('; ')
    if (!text)
      return []
    const heading = question?.question || id
    return [
      heading
        ? `${heading}: ${text}`
        : text,
    ]
  }).join('\n')
}

/**
 * The words of the button that sent a tool approval's answer.
 *
 * The answer is the chosen option's own word, which the recorded prompt offers as the
 * option's id. The decision row draws its allow and its refusal as the shared Allow and
 * Deny, so one answer reads the same before and after the reader gives it. A value
 * that the prompt does not offer reads as nothing.
 */
function approvalSummary(response: Record<string, unknown>, options: readonly PermissionOption[]): ControlResponseSummary | null {
  const value = pickString(response, OH_MY_PI_DIALOG_RESPONSE.Value)
  const option = options.find(candidate => candidate.optionId === value)
  if (!option)
    return null
  if (isAllowPermissionKind(option.kind))
    return label(CONTROL_DECISION_WORDS.permission.allow)
  return isRejectPermissionKind(option.kind)
    ? label(CONTROL_DECISION_WORDS.permission.deny)
    : null
}

/**
 * Read the decision or the answer a saved omp response states.
 *
 * The prompt that the Worker recorded with the answer states a tool approval as a
 * permission. Every other answer states its own shape.
 */
export function ohMyPiControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const response = cr.response
  if (!response)
    return null
  if (response[OH_MY_PI_DIALOG_RESPONSE.Cancelled] === true)
    return label('Cancelled')
  if (response[OH_MY_PI_ASK_ENVELOPE.Type] === OH_MY_PI_ASK_TYPE.Answer) {
    const questions = cr.prompt?.kind === 'question'
      ? cr.prompt.questions
      : []
    const lines = askAnswerLines(response, questions)
    return label(lines || 'Answered')
  }
  if (cr.prompt?.kind === 'permission')
    return approvalSummary(response, cr.prompt.permission.options)
  const confirmed = response[OH_MY_PI_DIALOG_RESPONSE.Confirmed]
  if (typeof confirmed === 'boolean')
    return label(confirmed ? 'Confirmed' : 'Declined')
  const value = response[OH_MY_PI_DIALOG_RESPONSE.Value]
  if (typeof value === 'string') {
    return label(value === ''
      ? 'Empty answer'
      : value)
  }
  return null
}
