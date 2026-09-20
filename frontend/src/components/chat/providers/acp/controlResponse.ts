import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import {
  CANONICAL_KINDS,
  KIND_ALLOW_ALWAYS,
  KIND_ALLOW_ONCE,
  KIND_REJECT_ONCE,
} from '../../model/controlPrompt'
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
 *
 * Exported for its OWN test. `permissionOptionLabel` is the one caller today, and its
 * own `Object.hasOwn` hides a bad answer here: a kind that is a function matches no
 * fallback, so the label falls through to the raw id and the row reads the same either
 * way. A second caller that reads the kind directly has no such cover, so the contract
 * is pinned here rather than through a reader that cannot see it break.
 */
export function acpOptionIdKind(optionId: string): string {
  if (CANONICAL_KINDS.includes(optionId))
    return optionId
  // `Object.hasOwn`, not `??`: the id comes straight off the wire, and one that spells
  // an `Object.prototype` member resolves to that function. A function is truthy, so
  // `??` returned it as the KIND and the next caller that reads a kind without
  // `Object.hasOwn` of its own draws the function's source text, or calls it.
  return Object.hasOwn(OPTION_ID_KINDS, optionId) ? OPTION_ID_KINDS[optionId] ?? '' : ''
}

/**
 * Resolve the selected `optionId` (from `result.outcome.optionId`) to the words
 * the decision button carried.
 *
 * The option comes from the request's own list, and its label comes from
 * `permissionOptionLabel`, which prefers the agent's display name and reads the option
 * `kind` when the agent named the option after its own id. Without the request, the
 * optionId still states a kind whenever it is one LeapMux can read (see
 * {@link acpOptionIdKind}); anything else keeps the raw id, which states what the user sent
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
    // An empty name is no name: `permissionOptionLabel` would draw it as the button's
    // words, and its kind fallback is the truthful answer instead.
    const name = pickString(option, 'name', '').trim()
    return permissionOptionLabel({
      optionId,
      kind: pickString(option, 'kind', '').trim(),
      ...(name !== '' ? { name } : {}),
    })
  }

  return permissionOptionLabel({ optionId, kind: acpOptionIdKind(optionId) })
}

/**
 * The default ACP control-response derivation (the permission-selection path). Providers that also
 * speak a question protocol (OpenCode/Kilo) or a bespoke flow (Cursor) wrap this with their own
 * dispatch and delegate here for the permission case.
 */
export function acpControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  return labelOrNull(acpPermissionResponseText(cr.request, cr.response))
}
