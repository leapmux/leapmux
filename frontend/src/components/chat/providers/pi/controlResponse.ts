/**
 * The display of a saved Pi `extension_ui_response`.
 *
 * Pi blocks until the client posts a matching response line on stdin. The Worker
 * builds that line from the reader's decision, and the saved row keeps it. The
 * native dialog methods take these response shapes:
 *
 *   select / input / editor → { type, id, value }
 *   confirm                 → { type, id, confirmed }
 *   any cancel              → { type, id, cancelled: true }
 *
 * The prompt that the Worker recorded with the answer states which control the reader
 * answered (`ReadControlRequest` in the Go `pi` package): the plan menu, a question
 * (an `input` or a `select`), or a dialog.
 */

import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { label } from '../../persistedControlResponse'

/** The exact text that a value answer carried, or a word for the explicit empty answer. */
function valueSummary(response: Record<string, unknown>): ControlResponseSummary | null {
  if (typeof response.value !== 'string')
    return null
  return label(response.value === ''
    ? 'Empty answer'
    : response.value)
}

/** The decision that a confirm answer carried. A confirm requires an explicit one. */
function confirmSummary(response: Record<string, unknown>): ControlResponseSummary | null {
  if (typeof response.confirmed !== 'boolean')
    return null
  return label(response.confirmed
    ? 'Approved'
    : 'Rejected')
}

/**
 * The answer to the plan menu. Its three plan actions approve or refuse the plan, and
 * any other option is an extension's own choice, which reads as the option itself.
 */
function planSummary(response: Record<string, unknown>): ControlResponseSummary | null {
  switch (response.value) {
    case PI_PLAN_ACTION.ImplementHere:
    case PI_PLAN_ACTION.ImplementFresh:
      return label('Approved')
    case PI_PLAN_ACTION.Stay:
      return label('Rejected')
    default:
      return valueSummary(response)
  }
}

/** Read the native decision or exact text from a saved Pi response. */
export function piControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const response = cr.response
  if (!response)
    return null
  // Every control takes a cancellation, so it needs no prompt to read.
  if (response.cancelled === true)
    return label('Cancelled')
  const prompt = cr.prompt
  switch (prompt?.kind) {
    case 'plan':
      return planSummary(response)
    case 'question':
      return valueSummary(response)
    case 'dialog':
      return prompt.dialog.variant === 'confirm'
        ? confirmSummary(response)
        : valueSummary(response)
    default:
      return null
  }
}
