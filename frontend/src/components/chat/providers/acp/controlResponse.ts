import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { labelOrNull } from '../../persistedControlResponse'

/**
 * Resolve the selected `optionId` (from `result.outcome.optionId`) to its option `name` in the
 * request's option list, falling back to the well-known-kind map (once/always/reject/cancel) and
 * finally the raw optionId. Null when no optionId was selected.
 */
export function acpPermissionResponseText(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  const outcome = pickObject(result, 'outcome', undefined)
  const optionId = pickString(outcome, 'optionId', '').trim()
  if (!optionId)
    return null

  const params = pickObject(request, 'params', undefined)
  const options = Array.isArray(params?.options) ? params.options : []
  for (const option of options) {
    if (!isObject(option))
      continue
    if (pickString(option, 'optionId', '').trim() === optionId) {
      const name = pickString(option, 'name', '').trim()
      if (name)
        return name
      break
    }
  }

  switch (optionId) {
    case 'once':
    case 'proceed_once':
      return 'Allow once'
    case 'always':
    case 'proceed_always':
      return 'Always allow'
    case 'reject':
    case 'cancel':
      return 'Reject'
    default:
      return optionId
  }
}

/**
 * The default ACP control-response derivation (the permission-selection path). Providers that also
 * speak a question protocol (OpenCode/Kilo) or a bespoke flow (Cursor) wrap this with their own
 * dispatch and delegate here for the permission case.
 */
export function acpControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  return labelOrNull(acpPermissionResponseText(cr.request, cr.response))
}
