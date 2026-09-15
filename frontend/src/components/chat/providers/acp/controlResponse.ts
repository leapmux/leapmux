import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import {
  CANONICAL_KINDS,
  KIND_ALLOW_ALWAYS,
  KIND_ALLOW_ONCE,
  KIND_REJECT_ONCE,
  permissionOptionLabel,
} from '../../controls/permissionOptionLabels'
import { labelOrNull } from '../../persistedControlResponse'

/**
 * The kind a well-known optionId implies, for a request LeapMux no longer holds.
 *
 * An agent spells its own option ids, so this covers the ids LeapMux saw rather than
 * every id that exists. An id outside it resolves to no kind, and the raw id is then
 * the only truthful answer.
 */
const OPTION_ID_KINDS: Record<string, string> = {
  once: KIND_ALLOW_ONCE,
  proceed_once: KIND_ALLOW_ONCE,
  always: KIND_ALLOW_ALWAYS,
  proceed_always: KIND_ALLOW_ALWAYS,
  reject: KIND_REJECT_ONCE,
  cancel: KIND_REJECT_ONCE,
}

/**
 * The kind an optionId states on its own, without the request's option list.
 *
 * The protocol's four kind tokens are option ids as well: an agent that spells no
 * vocabulary of its own answers with them directly, which Goose and Reasonix both do. The
 * id then names its own kind, and reading it is not a guess about the agent's intent --
 * it is the protocol's own word for that intent.
 */
function optionIdKind(optionId: string): string {
  if (CANONICAL_KINDS.includes(optionId))
    return optionId
  return OPTION_ID_KINDS[optionId] ?? ''
}

/**
 * Resolve the selected `optionId` (from `result.outcome.optionId`) to the words
 * the decision button carried.
 *
 * The option comes from the request's own list, and its label comes from
 * `permissionOptionLabel`, which prefers the agent's display name and reads the option
 * `kind` when the agent named the option after its own id. Without the request, the
 * optionId still states a kind whenever it is one LeapMux can read (see
 * {@link optionIdKind}); anything else keeps the raw id, which states what the user sent
 * and invents nothing. Null when no optionId was selected.
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
    if (pickString(option, 'optionId', '').trim() !== optionId)
      continue
    return permissionOptionLabel({
      optionId,
      kind: pickString(option, 'kind', '').trim(),
      name: pickString(option, 'name', '').trim() || undefined,
    })
  }

  return permissionOptionLabel({ optionId, kind: optionIdKind(optionId) })
}

/**
 * The default ACP control-response derivation (the permission-selection path). Providers that also
 * speak a question protocol (OpenCode/Kilo) or a bespoke flow (Cursor) wrap this with their own
 * dispatch and delegate here for the permission case.
 */
export function acpControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  return labelOrNull(acpPermissionResponseText(cr.request, cr.response))
}
