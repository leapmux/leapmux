import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { ACP_PERMISSION_KIND, ACP_PERMISSION_OUTCOME } from '~/generated/contracts/acp-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import {
  CANONICAL_KINDS,
  KIND_ALLOW_ALWAYS,
  KIND_REJECT_ONCE,
} from '../../model/controlPrompt'
import { labelOrNull } from '../../persistedControlResponse'

/**
 * The tool call that one stored `session/request_permission` is about, as the request
 * itself states it, or undefined for another shape.
 */
export function acpPermissionToolCall(payload: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  return pickObject(pickObject(payload, 'params'), 'toolCall') ?? undefined
}

/**
 * The options that one stored `session/request_permission` offered.
 *
 * Every Agent Client Protocol provider sends its own vocabulary of option ids, so a
 * display reads an option's KIND rather than its id. A permission's display reads the
 * options of the recorded prompt instead; this serves a question or a plan approval
 * that rides a permission request, whose prompt records none of the request's options.
 *
 * READ, never asserted: `options` is wire data. A STRING `options` has a `length` of
 * its own, so a reader that asserted an array iterated its characters. An element
 * that is no object cannot state a kind, so it is dropped, and an empty name is no
 * name, because `permissionOptionLabel` would draw it as the button's words.
 */
export function acpPermissionOptions(payload: Record<string, unknown> | undefined): PermissionOption[] {
  const options = pickObject(payload, 'params')?.options
  if (!Array.isArray(options))
    return []
  return options.filter(isObject).map((option) => {
    const name = pickString(option, 'name')
    return {
      optionId: pickString(option, 'optionId'),
      kind: pickString(option, 'kind'),
      ...(name !== ''
        ? { name }
        : {}),
    }
  })
}

/**
 * The kind a well-known optionId implies, for a request LeapMux no longer holds.
 *
 * An agent spells its own option ids, so this covers the ids LeapMux saw rather than
 * every id that exists. An id outside it resolves to no kind, and the raw id is then
 * the only truthful answer.
 */
const OPTION_ID_KINDS: Record<string, string> = {
  once: ACP_PERMISSION_KIND.AllowOnce,
  proceed_once: ACP_PERMISSION_KIND.AllowOnce,
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
 * Exported for Qwen Code's plan display, which reads the kind directly, and for its
 * OWN test. `permissionOptionLabel` has an `Object.hasOwn` of its own that hides a bad
 * answer here: a kind that is a function matches no fallback, so the label falls
 * through to the raw id and the row reads the same either way. A caller that reads the
 * kind directly has no such cover, so the contract is pinned here rather than through
 * a reader that cannot see it break.
 */
export function acpOptionIdKind(optionId: string): string {
  if (CANONICAL_KINDS.includes(optionId))
    return optionId
  // `Object.hasOwn`, not `??`: the id comes straight off the wire, and one that spells
  // an `Object.prototype` member resolves to that function. A function is truthy, so
  // `??` returned it as the KIND and the next caller that reads a kind without
  // `Object.hasOwn` of its own draws the function's source text, or calls it.
  return Object.hasOwn(OPTION_ID_KINDS, optionId)
    ? OPTION_ID_KINDS[optionId] ?? ''
    : ''
}

/**
 * Resolve the selected `optionId` (from `result.outcome.optionId`) to the words
 * the decision button carried.
 *
 * The option comes from `options`, the list that the button row drew: the options of
 * the permission prompt that the Worker recorded with the answer. Its label comes from
 * `permissionOptionLabel`, which prefers the agent's display name and reads the option
 * `kind` when the agent named the option after its own id. An option that the list
 * does not hold still states a kind whenever its id is one LeapMux can read (see
 * {@link acpOptionIdKind}); anything else keeps the raw id, which states what the user
 * sent and invents nothing.
 *
 * The protocol's `cancelled` outcome selects no option. It reads as "Cancelled": the
 * reply to a typed reason when the request offered no reject option, and the answer
 * that the worker stores when a stop withdraws the request. Null when the response
 * holds neither.
 */
export function acpPermissionResponseText(
  options: readonly PermissionOption[],
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  const outcome = pickObject(result, 'outcome', undefined)
  const optionId = pickString(outcome, 'optionId', '').trim()
  if (!optionId) {
    return pickString(outcome, 'outcome', '') === ACP_PERMISSION_OUTCOME.Cancelled
      ? 'Cancelled'
      : null
  }
  const option = options.find(candidate => candidate.optionId === optionId)
  return permissionOptionLabel(option ?? {
    optionId,
    kind: acpOptionIdKind(optionId),
  })
}

/**
 * The default ACP control-response derivation (the permission-selection path). Providers that also
 * speak a question protocol (OpenCode/Kilo) or a bespoke flow (Cursor) wrap this with their own
 * dispatch and delegate here for the permission case.
 */
export function acpControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  // A permission prompt holds the options that the buttons drew. A question or a plan
  // that rides a permission request records none of the request's own options, such as
  // the one that dismisses a question, so those take the options of the request.
  const options = cr.prompt?.kind === 'permission'
    ? cr.prompt.permission.options
    : acpPermissionOptions(cr.request)
  return labelOrNull(acpPermissionResponseText(options, cr.response))
}
