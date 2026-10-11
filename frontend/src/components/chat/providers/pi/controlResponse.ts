/**
 * Build Pi extension_ui_response bodies.
 *
 * Pi blocks until the client posts a matching response line on stdin.
 * The native dialog methods use these response shapes:
 *
 *   select / input / editor → { type, id, value }
 *   confirm                 → { type, id, confirmed }
 *   any cancel              → { type, id, cancelled: true }
 *
 * SendControlResponse sends UTF-8 bytes. The worker's providerkit.Process.SendRawInput
 * adds a trailing newline before it writes to Pi's stdin.
 */

import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { sendResponse } from '../../controls/types'
import { label } from '../../persistedControlResponse'
import { isPiPlanApproval } from './planRequest'

const RESPONSE_TYPE = PI_EVENT.ExtensionUIResponse

export interface PiSelectResponse {
  type: typeof RESPONSE_TYPE
  id: string
  value: string
}

export interface PiConfirmResponse {
  type: typeof RESPONSE_TYPE
  id: string
  confirmed: boolean
}

export interface PiCancelledResponse {
  type: typeof RESPONSE_TYPE
  id: string
  cancelled: true
}

export type PiExtensionResponse = PiSelectResponse | PiConfirmResponse | PiCancelledResponse

export function piValueResponse(requestId: string, value: string): PiSelectResponse {
  return { type: RESPONSE_TYPE, id: requestId, value }
}

export function piConfirmResponse(requestId: string, confirmed: boolean): PiConfirmResponse {
  return { type: RESPONSE_TYPE, id: requestId, confirmed }
}

export function piCancelResponse(requestId: string): PiCancelledResponse {
  return { type: RESPONSE_TYPE, id: requestId, cancelled: true }
}

/**
 * Read the current answer from ControlAnswerState.
 * Prefer the first selected option. Use the first custom-text entry when no option exists.
 */
export function piAskAnswerValue(answerState: ControlAnswerState, questions?: ControlQuestion[], payload?: Record<string, unknown>): string {
  const selections = answerState.selections()[0] ?? []
  if (selections.length) {
    const multiSelect = questions?.[0]?.multiSelect || (payload?.method === PI_DIALOG_METHOD.Input && payload.placeholder === '1,3')
    // The length test above pinned the first selection; `?? ''` is the type-level guard alone.
    return multiSelect ? selections.join(',') : selections[0] ?? ''
  }
  return answerState.customTexts()[0] ?? ''
}

/**
 * Send an extension_ui_response to Pi.
 * The control banner supplies onRespond, which calls workerRpc.sendControlResponse.
 */
export function sendPiExtensionResponse(
  onRespond: ControlResponseSender,
  response: PiExtensionResponse,
): Promise<void> {
  return sendResponse(onRespond, response)
}

/** Read the native decision or exact text from a saved Pi response. */
export function piControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const response = cr.response
  if (!response)
    return null
  if (response.cancelled === true)
    return label('Cancelled')

  if (cr.request && isPiPlanApproval(cr.request)) {
    if (response.value === PI_PLAN_ACTION.ImplementHere || response.value === PI_PLAN_ACTION.ImplementFresh)
      return label('Approved')
    if (response.value === PI_PLAN_ACTION.Stay)
      return label('Rejected')
  }

  const method = pickString(cr.request, 'method', '')
  if (method === PI_DIALOG_METHOD.Confirm || (method === '' && typeof response.confirmed === 'boolean')) {
    return typeof response.confirmed === 'boolean'
      ? label(response.confirmed ? 'Approved' : 'Rejected')
      : null
  }
  switch (method) {
    case PI_DIALOG_METHOD.Select:
    case PI_DIALOG_METHOD.Input:
    case PI_DIALOG_METHOD.Editor:
    case '': {
      return typeof response.value === 'string' ? label(response.value === '' ? 'Empty answer' : response.value) : null
    }
    default:
      return null
  }
}
