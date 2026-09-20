import type { DialogPrompt } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { PI_DIALOG_METHOD } from '~/generated/contracts/pi-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { isPiPlanApproval, piPlanApprovalDetails } from './planRequest'

/**
 * Which control the reader answers a Pi dialog with.
 *
 * Pi declares four methods and LeapMux draws three controls: `confirm` is two
 * buttons, `input` is one line beside them, and `editor` is a text area above them.
 * `select` reaches here only for a shape `isPiPlanApproval` refused, and it has no
 * control of its own -- the reader acknowledges it, which is what a bare confirm is.
 */
function piDialogVariant(method: string): DialogPrompt['variant'] {
  switch (method) {
    case PI_DIALOG_METHOD.Input:
      return 'input'
    case PI_DIALOG_METHOD.Editor:
      return 'editor'
    default:
      return 'confirm'
  }
}

/** `Provider.extractControl` for Pi. */
export function piExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (isPiPlanApproval(payload))
    return { kind: 'plan', details: piPlanApprovalDetails(payload) }
  const timeout = pickNumber(payload, 'timeout')
  const message = pickString(payload, 'message', undefined)
  const placeholder = pickString(payload, 'placeholder', undefined)
  const prefill = pickString(payload, 'prefill', undefined)
  return {
    kind: 'dialog',
    dialog: {
      title: pickString(payload, 'title') || 'Approval Required',
      ...(message !== undefined ? { message } : {}),
      ...(placeholder !== undefined ? { placeholder } : {}),
      ...(prefill !== undefined ? { prefill } : {}),
      variant: piDialogVariant(pickString(payload, 'method')),
      // A dialog that answers ITSELF after a wait is one the reader must be able to
      // see a deadline on. Zero and below state no deadline at all.
      ...(timeout != null && timeout > 0 ? { timeoutMs: timeout } : {}),
    },
  }
}
