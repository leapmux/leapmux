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

import type { AskQuestionState } from '../../controls/types'
import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { pickString } from '~/lib/jsonPick'
import { sendResponse } from '../../controls/types'
import { label } from '../../persistedControlResponse'
import { PI_DIALOG_METHOD, PI_EVENT } from './protocol'

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
 * Resolve the current answer value from a shared AskQuestionState — prefers
 * the first selected option, falling back to the first custom-text entry.
 */
export function piAskAnswerValue(askState: AskQuestionState): string {
  const selection = askState.selections()[0]?.[0]
  if (selection)
    return selection
  return askState.customTexts()[0] ?? ''
}

/**
 * Sends an extension_ui_response back to the running Pi agent. `onRespond` is
 * the shared sender supplied by the control-bubble harness — it ultimately
 * calls workerRpc.sendControlResponse.
 */
export function sendPiExtensionResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  response: PiExtensionResponse,
): Promise<void> {
  return sendResponse(agentId, onRespond, response)
}

/**
 * Derive the display for a persisted Pi extension-UI response. A cancel is "Cancelled"; a confirm
 * dialog maps to "Approve"/"Deny"; a select/input/editor dialog shows the typed value. The pruned
 * request method disambiguates confirm-vs-value precisely, but when it is absent (a request-gone
 * answer) the response shape recovers it: a `confirmed` boolean is a confirm dialog, a non-empty
 * `value` is a text dialog. Null for an empty value or an unrecognized known method (the caller then
 * degrades to the generic label).
 */
export function piControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  const response = cr.response
  if (!response)
    return null
  if (response.cancelled === true)
    return label('Cancelled')

  const method = pickString(cr.request, 'method', '')
  if (method === PI_DIALOG_METHOD.Confirm || (method === '' && typeof response.confirmed === 'boolean'))
    return label(response.confirmed === true ? 'Approve' : 'Deny')
  switch (method) {
    case PI_DIALOG_METHOD.Select:
    case PI_DIALOG_METHOD.Input:
    case PI_DIALOG_METHOD.Editor:
    case '': {
      const value = pickString(response, 'value', '').trim()
      return value ? label(value) : null
    }
    default:
      return null
  }
}
