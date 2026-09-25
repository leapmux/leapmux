import type { ControlResponseSender } from '../../controls/types'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { OH_MY_PI_APPROVAL_DIALOG, OH_MY_PI_ASK_ANSWER, OH_MY_PI_ASK_ENVELOPE, OH_MY_PI_ASK_TYPE, OH_MY_PI_DIALOG_METHOD, OH_MY_PI_DIALOG_RESPONSE, OH_MY_PI_EVENT } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { sendResponse } from '../../controls/types'
import { CONTROL_DECISION_WORDS, label } from '../../persistedControlResponse'

/**
 * The answers omp's dialogs take, as `extension_ui_response` lines on its stdin:
 *
 *   select / input / editor -> {type, id, value}
 *   confirm                 -> {type, id, confirmed}
 *   any dismissal           -> {type, id, cancelled: true}
 *
 * The question bridge takes LeapMux's own answer envelope instead
 * (`leapmux_ask_answer`), which the worker turns into the dialog chain omp waits on.
 */
export type OhMyPiDialogResponse = Record<string, unknown>

export function ohMyPiValueResponse(requestId: string, value: string): OhMyPiDialogResponse {
  return {
    [OH_MY_PI_DIALOG_RESPONSE.Type]: OH_MY_PI_EVENT.ExtensionUIResponse,
    [OH_MY_PI_DIALOG_RESPONSE.ID]: requestId,
    [OH_MY_PI_DIALOG_RESPONSE.Value]: value,
  }
}

export function ohMyPiConfirmResponse(requestId: string, confirmed: boolean): OhMyPiDialogResponse {
  return {
    [OH_MY_PI_DIALOG_RESPONSE.Type]: OH_MY_PI_EVENT.ExtensionUIResponse,
    [OH_MY_PI_DIALOG_RESPONSE.ID]: requestId,
    [OH_MY_PI_DIALOG_RESPONSE.Confirmed]: confirmed,
  }
}

export function ohMyPiCancelResponse(requestId: string): OhMyPiDialogResponse {
  return {
    [OH_MY_PI_DIALOG_RESPONSE.Type]: OH_MY_PI_EVENT.ExtensionUIResponse,
    [OH_MY_PI_DIALOG_RESPONSE.ID]: requestId,
    [OH_MY_PI_DIALOG_RESPONSE.Cancelled]: true,
  }
}

/** One question's answer in the bridge's envelope: the chosen labels, and the typed text. */
export interface OhMyPiAskQuestionAnswer {
  id: string
  selected: string[]
  custom: string
}

/** The bridge's answer to one question request. */
export function ohMyPiAskAnswer(requestId: string, answers: OhMyPiAskQuestionAnswer[]): Record<string, unknown> {
  return {
    [OH_MY_PI_ASK_ENVELOPE.Type]: OH_MY_PI_ASK_TYPE.Answer,
    [OH_MY_PI_ASK_ENVELOPE.ID]: requestId,
    [OH_MY_PI_ASK_ENVELOPE.Answers]: answers.map(answer => ({
      [OH_MY_PI_ASK_ANSWER.ID]: answer.id,
      ...(answer.selected.length > 0 ? { [OH_MY_PI_ASK_ANSWER.Selected]: answer.selected } : {}),
      ...(answer.custom.trim() ? { [OH_MY_PI_ASK_ANSWER.Custom]: answer.custom } : {}),
    })),
  }
}

/** Send one answer to omp through the shared control-response channel. */
export function sendOhMyPiResponse(onRespond: ControlResponseSender, response: Record<string, unknown>): Promise<void> {
  return sendResponse(onRespond, response)
}

/** Whether one stored control request is omp's tool approval dialog. */
export function isOhMyPiApproval(payload: Record<string, unknown> | undefined): boolean {
  if (!payload || payload.type !== OH_MY_PI_EVENT.ExtensionUIRequest || payload.method !== OH_MY_PI_DIALOG_METHOD.Select)
    return false
  const options = stringArray(payload.options)
  return pickString(payload, 'title').startsWith(OH_MY_PI_APPROVAL_DIALOG.TitlePrefix)
    && options.includes(OH_MY_PI_APPROVAL_DIALOG.Approve)
    && options.includes(OH_MY_PI_APPROVAL_DIALOG.Deny)
}

/**
 * The bridge's saved answer, one `question: answer` line each.
 *
 * The bridge states every question of the call, the ones the reader left blank
 * included. A blank question has no line, so a heading never stands with no answer
 * after it.
 */
function askAnswerLines(response: Record<string, unknown>, request: Record<string, unknown> | undefined): string {
  const answers = Array.isArray(response[OH_MY_PI_ASK_ENVELOPE.Answers]) ? (response[OH_MY_PI_ASK_ENVELOPE.Answers] as unknown[]).filter(isObject) : []
  const questions = Array.isArray(request?.[OH_MY_PI_ASK_ENVELOPE.Questions]) ? (request[OH_MY_PI_ASK_ENVELOPE.Questions] as unknown[]).filter(isObject) : []
  return answers.flatMap((answer) => {
    const text = [stringArray(answer[OH_MY_PI_ASK_ANSWER.Selected]).join(', '), pickString(answer, OH_MY_PI_ASK_ANSWER.Custom)]
      .filter(part => part.trim() !== '')
      .join('; ')
    if (!text)
      return []
    const id = pickString(answer, OH_MY_PI_ASK_ANSWER.ID)
    const question = questions.find(record => pickString(record, 'id') === id)
    const heading = pickString(question, 'question') || id
    return [heading ? `${heading}: ${text}` : text]
  }).join('\n')
}

/** Read the decision or the answer a saved omp response states. */
export function ohMyPiControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const response = cr.response
  if (!response)
    return null
  if (response[OH_MY_PI_DIALOG_RESPONSE.Cancelled] === true)
    return label('Cancelled')
  if (response[OH_MY_PI_ASK_ENVELOPE.Type] === OH_MY_PI_ASK_TYPE.Answer) {
    const lines = askAnswerLines(response, cr.request)
    return label(lines || 'Answered')
  }
  // The words the approval's own buttons carry, so one answer reads the same before
  // and after the reader gives it.
  if (isOhMyPiApproval(cr.request)) {
    const value = pickString(response, OH_MY_PI_DIALOG_RESPONSE.Value)
    if (value === OH_MY_PI_APPROVAL_DIALOG.Approve)
      return label(CONTROL_DECISION_WORDS.permission.allow)
    if (value === OH_MY_PI_APPROVAL_DIALOG.Deny)
      return label(CONTROL_DECISION_WORDS.permission.deny)
    return null
  }
  const confirmed = response[OH_MY_PI_DIALOG_RESPONSE.Confirmed]
  if (typeof confirmed === 'boolean')
    return label(confirmed ? 'Confirmed' : 'Declined')
  const value = response[OH_MY_PI_DIALOG_RESPONSE.Value]
  if (typeof value === 'string')
    return label(value === '' ? 'Empty answer' : value)
  return null
}
