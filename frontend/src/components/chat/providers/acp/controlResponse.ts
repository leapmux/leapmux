import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { ControlExtractionInput, ExtractedControlRequest } from '../capabilities'
import { ACP_PERMISSION_OUTCOME } from '~/generated/contracts/acp-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { buildJsonRpcResult } from '../../controls/types'
import {
  CANONICAL_KINDS,
  KIND_ALLOW_ALWAYS,
  KIND_ALLOW_ONCE,
  KIND_REJECT_ALWAYS,
  KIND_REJECT_ONCE,
} from '../../model/controlPrompt'
import { labelOrNull } from '../../persistedControlResponse'
import { acpPermissionOptions } from './extractControl'

/**
 * Adds a typed reason to the reply that rejects a permission, in the provider's own
 * field. It returns the reply's `result`.
 */
export type ACPPermissionRejectReason = (result: Record<string, unknown>, reason: string) => Record<string, unknown>

/** What the reply builder of the composer knows about one provider. */
export interface ACPReplyPolicy {
  /** The request is a plan approval, which the shared plan row answers. */
  isPlanApproval: (payload: Record<string, unknown>) => boolean
  /**
   * The permission options of a request, as the provider's own reader draws them on
   * the banner. The reply answers with the same options, so the composer and the
   * banner cannot answer one request in two shapes. Without it, the reply reads the
   * request's own option list.
   */
  permissionOptions?: (payload: Record<string, unknown>) => readonly PermissionOption[]
  /**
   * The provider's own field for the reason of a rejected permission. Without one,
   * the reason reaches the agent as a message of its own once the reply went.
   */
  permissionRejectReason?: ACPPermissionRejectReason
}

/** The first option of the kinds, in the order the kinds are given. */
function firstOptionOfKinds(options: readonly PermissionOption[], kinds: readonly string[]): string | undefined {
  for (const kind of kinds) {
    const option = options.find(candidate => candidate.kind === kind && candidate.optionId !== '')
    if (option)
      return option.optionId
  }
  return undefined
}

/**
 * The reply that the composer sends for an Agent Client Protocol control request: the
 * text the reader typed rejects the request, and an empty composer approves it.
 *
 *   - A PLAN APPROVAL always rejects, with the reason when there is one. The approval
 *     button owns the allow path, and the worker writes the provider's own reply.
 *   - A PERMISSION answers with one of its own options, because the agent reads
 *     nothing else: a reject option, else the protocol's `cancelled` outcome, and for
 *     an empty composer an allow option. The options are the ones that the banner
 *     draws (see {@link ACPReplyPolicy.permissionOptions}). A permission with no
 *     allow option has nothing that an empty composer can send, so the reply is
 *     undefined and the composer keeps the draft. The reason rides in the provider's
 *     own field when it has one (see {@link acpControlFeedbackAsFollowUpMessage}).
 *   - Anything else takes the shared allow and deny envelope, which the provider's
 *     worker rewrites.
 */
export function acpBuildControlResponse(
  payload: Record<string, unknown>,
  content: string,
  requestId: string,
  policy: ACPReplyPolicy,
): Record<string, unknown> | undefined {
  if (policy.isPlanApproval(payload))
    return buildDenyResponse(requestId, content)
  const options = permissionOptionsOf(payload, policy)
  if (options.length > 0) {
    if (!content) {
      const allow = firstOptionOfKinds(options, [KIND_ALLOW_ONCE, KIND_ALLOW_ALWAYS])
      return allow !== undefined
        ? buildJsonRpcResult(requestId, { outcome: { outcome: ACP_PERMISSION_OUTCOME.Selected, optionId: allow } })
        : undefined
    }
    else {
      const reject = firstOptionOfKinds(options, [KIND_REJECT_ONCE, KIND_REJECT_ALWAYS])
      const result: Record<string, unknown> = reject !== undefined
        ? { outcome: { outcome: ACP_PERMISSION_OUTCOME.Selected, optionId: reject } }
        : { outcome: { outcome: ACP_PERMISSION_OUTCOME.Cancelled } }
      return buildJsonRpcResult(requestId, policy.permissionRejectReason ? policy.permissionRejectReason(result, content) : result)
    }
  }
  return content
    ? buildDenyResponse(requestId, content)
    : buildAllowResponse(requestId, getToolInput(payload))
}

/**
 * Whether the reason the reader typed must follow a reply as a message of its own.
 *
 * It must for a permission whose provider has no field for it: the reply selects one
 * of the agent's options, and an option carries no text. A plan approval carries its
 * reason to the worker, which writes it into the provider's reply or queues it.
 */
export function acpControlFeedbackAsFollowUpMessage(payload: Record<string, unknown>, policy: ACPReplyPolicy): boolean {
  return !policy.isPlanApproval(payload) && permissionOptionsOf(payload, policy).length > 0 && policy.permissionRejectReason === undefined
}

/** The permission options that the policy reads for payload. */
function permissionOptionsOf(payload: Record<string, unknown>, policy: ACPReplyPolicy): readonly PermissionOption[] {
  return (policy.permissionOptions ?? acpPermissionOptions)(payload)
}

/** One provider's reader of its control requests, which the reply builder asks. */
export type ACPControlReader = (input: ControlExtractionInput) => ExtractedControlRequest | null

/**
 * The reply policy of one provider, from its own control reader.
 *
 * A request is a plan approval exactly when the reader draws it as one, and its
 * permission options are the ones that the reader draws, so the composer and the
 * banner can never disagree about which surface answers it or with which option.
 */
function acpReplyPolicy(extractControl: ACPControlReader, permissionRejectReason: ACPPermissionRejectReason | undefined): ACPReplyPolicy {
  return {
    isPlanApproval: payload => extractControl({ payload })?.kind === 'plan',
    permissionOptions: (payload) => {
      const control = extractControl({ payload })
      return control?.kind === 'permission' ? control.permission.options : []
    },
    ...(permissionRejectReason !== undefined ? { permissionRejectReason } : {}),
  }
}

/** The `buildControlResponse` hook of one provider: {@link acpBuildControlResponse} under its policy. */
export function acpControlResponseBuilder(extractControl: ACPControlReader, permissionRejectReason?: ACPPermissionRejectReason) {
  const policy = acpReplyPolicy(extractControl, permissionRejectReason)
  return (payload: Record<string, unknown>, content: string, requestId: string): Record<string, unknown> | undefined =>
    acpBuildControlResponse(payload, content, requestId, policy)
}

/** The `controlFeedbackAsFollowUpMessage` hook of one provider, under its policy. */
export function acpControlFeedbackRule(extractControl: ACPControlReader, permissionRejectReason?: ACPPermissionRejectReason) {
  const policy = acpReplyPolicy(extractControl, permissionRejectReason)
  return (payload: Record<string, unknown>): boolean => acpControlFeedbackAsFollowUpMessage(payload, policy)
}

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
 * and invents nothing.
 *
 * The protocol's `cancelled` outcome selects no option. It reads as "Cancelled": the
 * reply to a typed reason when the request offered no reject option, and the answer
 * that the worker stores when a stop withdraws the request. Null when the response
 * holds neither.
 */
export function acpPermissionResponseText(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  const outcome = pickObject(result, 'outcome', undefined)
  const optionId = pickString(outcome, 'optionId', '').trim()
  if (!optionId)
    return pickString(outcome, 'outcome', '') === ACP_PERMISSION_OUTCOME.Cancelled ? 'Cancelled' : null

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
