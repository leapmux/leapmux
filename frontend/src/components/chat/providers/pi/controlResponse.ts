/**
 * Helpers for building Pi extension_ui_response bodies.
 *
 * Pi's extension UI sub-protocol blocks the agent in `select`, `confirm`,
 * `input`, and `editor` dialogs until the client posts a matching response
 * line on stdin. The wire shapes are:
 *
 *   select / input / editor → { type, id, value }
 *   confirm                 → { type, id, confirmed }
 *   any cancel              → { type, id, cancelled: true }
 *
 * The response is encoded as UTF-8 bytes and shipped via the shared
 * SendControlResponse RPC; the worker's processBase.SendRawInput appends a
 * trailing newline before forwarding to Pi's stdin.
 */

import type { ControlAnswerState, Question } from '../../controls/types'
import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { PI_DIALOG_METHOD, PI_EVENT, PI_MCP_APPROVAL_CHOICE, PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { sendResponse } from '../../controls/types'
import { label } from '../../persistedControlResponse'
import { isPiMcpApproval } from './mcpApproval'
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
 * Resolve the current answer value from a shared ControlAnswerState — prefers
 * the first selected option, falling back to the first custom-text entry.
 */
export function piAskAnswerValue(answerState: ControlAnswerState, questions?: Question[], payload?: Record<string, unknown>): string {
  const selections = answerState.selections()[0] ?? []
  if (selections.length) {
    const multiSelect = questions?.[0]?.multiSelect || (payload?.method === PI_DIALOG_METHOD.Input && payload.placeholder === '1,3')
    return multiSelect ? selections.join(',') : selections[0]
  }
  return answerState.customTexts()[0] ?? ''
}

/**
 * Sends an extension_ui_response back to the running Pi agent. `onRespond` is
 * the shared sender supplied by the control-bubble harness — it ultimately
 * calls workerRpc.sendControlResponse.
 */
export function sendPiExtensionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  response: PiExtensionResponse,
): Promise<void> {
  return sendResponse(onRespond, response)
}

/** Read the native decision or exact text from a saved Pi response. */
export function piControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  const response = cr.response
  if (!response)
    return null
  if (response.cancelled === true)
    return label('Cancelled')

  if (cr.request && isPiMcpApproval(cr.request)) {
    if (response.value === PI_MCP_APPROVAL_CHOICE.AllowOnce)
      return label('Approved')
    if (response.value === PI_MCP_APPROVAL_CHOICE.AllowForSession)
      return label('Approved for this session')
    if (response.value === PI_MCP_APPROVAL_CHOICE.Deny)
      return label('Rejected')
  }

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
