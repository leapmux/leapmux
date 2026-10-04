import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import type { OhMyPiAskQuestionAnswer } from './controlResponse'
import { OH_MY_PI_ASK_ENVELOPE, OH_MY_PI_ASK_TYPE, OH_MY_PI_DIALOG_METHOD, OH_MY_PI_EVENT } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { isOhMyPiApproval } from './controlResponse'
import { ohMyPiQuestionPrompts } from './extractors/question'

/** Whether one stored request is the question bridge's request for a whole `ask` call. */
export function isOhMyPiAskRequest(payload: Record<string, unknown>): boolean {
  return payload[OH_MY_PI_ASK_ENVELOPE.Type] === OH_MY_PI_ASK_TYPE.Request
}

/**
 * Whether one stored request is a dialog the reader answers through the question
 * form: a `select` that an extension raised, or an `ask` select the bridge handed
 * over because its shape was not one it could follow.
 *
 * The tool approval dialog is a `select` too, and it is excluded: it is a permission,
 * and it draws as one. A `confirm`, an `input` and an `editor` draw as a dialog of
 * their own (`ohMyPiExtractControl`), which states the draft, the hint and the
 * deadline that the question form has no place for.
 */
export function isOhMyPiDialogQuestion(payload: Record<string, unknown>): boolean {
  return payload.type === OH_MY_PI_EVENT.ExtensionUIRequest
    && payload.method === OH_MY_PI_DIALOG_METHOD.Select
    && !isOhMyPiApproval(payload)
}

/** The options of a `select` dialog, with the descriptions omp sends beside them. */
function selectOptions(payload: Record<string, unknown>): ControlQuestion['options'] {
  const details = Array.isArray(payload.optionDetails) ? payload.optionDetails : []
  return stringArray(payload.options).map((label, index) => {
    const detail = details[index]
    const description = isObject(detail) ? pickString(detail, 'description') : ''
    return { label, ...(description ? { description } : {}) }
  })
}

/**
 * The questions one stored request asks, in the shared form's shape.
 *
 * The bridge's request carries the whole `ask` call, and a `select` asks one
 * question with its options. No other request reaches the question form (see
 * `isOhMyPiDialogQuestion`), so any other request asks none.
 */
export function ohMyPiQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  if (isOhMyPiAskRequest(payload)) {
    return ohMyPiQuestionPrompts(payload[OH_MY_PI_ASK_ENVELOPE.Questions]).map(prompt => ({
      id: prompt.id,
      question: prompt.question,
      ...(prompt.header ? { header: prompt.header } : {}),
      options: prompt.options,
      ...(prompt.multiSelect ? { multiSelect: true } : {}),
    }))
  }
  if (payload.method !== OH_MY_PI_DIALOG_METHOD.Select)
    return []
  return [{ question: pickString(payload, 'title') || 'Choose an option', options: selectOptions(payload) }]
}

/** The bridge's answers, one per question, from the shared form's state. */
export function ohMyPiAskAnswers(questions: ControlQuestion[], answerState: ControlAnswerState): OhMyPiAskQuestionAnswer[] {
  const selections = answerState.selections()
  const customTexts = answerState.customTexts()
  return questions.map((question, index) => ({
    id: question.id ?? '',
    selected: selections[index] ?? [],
    custom: customTexts[index] ?? '',
  }))
}

/** The one answer a dialog question takes: the chosen option, else the typed text. */
export function ohMyPiDialogAnswer(answerState: ControlAnswerState): string {
  return answerState.selections()[0]?.[0] ?? answerState.customTexts()[0] ?? ''
}
