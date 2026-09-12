import type { WirePermissionOption } from '../../controls/permissionOptionLabels'
import { COPILOT_APPROVAL_SCOPE, COPILOT_DECISION, COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import {
  KIND_ALLOW_ALWAYS,
  KIND_ALLOW_ONCE,
  KIND_REJECT_ONCE,
} from '../../controls/permissionOptionLabels'
import { copilotEvent } from './protocol'

/**
 * The decisions one Copilot permission request offers, and the answer each one sends.
 *
 * This holds no component, because the SAVED decision reads the same list: the words
 * a finished row shows are the words its button carried, which is what keeps one
 * answer reading the same way before and after the reader gives it.
 */

/** The scope words the decision row sends, in the order their buttons appear. */
export const COPILOT_ALLOW_ONCE = COPILOT_APPROVAL_SCOPE.Once
export const COPILOT_ALLOW_SESSION = COPILOT_APPROVAL_SCOPE.Session
export const COPILOT_ALLOW_PROJECT = COPILOT_APPROVAL_SCOPE.Project
export const COPILOT_REJECT = 'reject'
const COPILOT_ALLOW_SCOPES = new Set<string>([COPILOT_ALLOW_ONCE, COPILOT_ALLOW_SESSION, COPILOT_ALLOW_PROJECT])

/**
 * The option id whose button sends one native decision word.
 *
 * The answer LeapMux sends carries a SCOPE (`{behavior:'allow', scope:'session'}`),
 * and the worker turns that into the runtime's own word before it stores the answer.
 * A saved row therefore holds the word, and this reads it back to the option.
 */
const DECISION_OPTION_IDS: Record<string, string> = {
  [COPILOT_DECISION.ApproveOnce]: COPILOT_ALLOW_ONCE,
  [COPILOT_DECISION.ApproveForSession]: COPILOT_ALLOW_SESSION,
  [COPILOT_DECISION.ApproveForLocation]: COPILOT_ALLOW_PROJECT,
  [COPILOT_DECISION.Reject]: COPILOT_REJECT,
}

/** The permission-option kind one native decision word belongs to, for a request that never offered it. */
const DECISION_KINDS: Record<string, string> = {
  [COPILOT_DECISION.ApproveOnce]: KIND_ALLOW_ONCE,
  [COPILOT_DECISION.ApproveForSession]: KIND_ALLOW_ALWAYS,
  [COPILOT_DECISION.ApproveForLocation]: KIND_ALLOW_ALWAYS,
  [COPILOT_DECISION.Reject]: KIND_REJECT_ONCE,
}

/** The option a saved decision word names, or the kind alone when the request never offered it. */
export function copilotDecisionOption(
  payload: Record<string, unknown> | undefined,
  decision: string,
): WirePermissionOption | undefined {
  const optionId = DECISION_OPTION_IDS[decision]
  if (optionId === undefined)
    return undefined
  const offered = payload ? copilotPermissionOptions(payload) : []
  return offered.find(option => option.optionId === optionId)
    ?? { optionId, kind: DECISION_KINDS[decision] ?? '' }
}

/** The permission request one control payload carries, or undefined for another kind. */
export function copilotPermission(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const event = copilotEvent(payload)
  if (!event || event.type !== COPILOT_EVENT.PermissionRequested)
    return undefined
  return pickObject(event.data, 'permissionRequest') ?? undefined
}

/**
 * Whether a session-wide approval is expressible for this request.
 *
 * The runtime needs an explicit approval RULE, not a bare "for this session": a bare
 * one did not retain the read approval in CP-007. The rules this build can construct
 * are read, write, shell and Model Context Protocol, and the runtime states for write
 * and shell whether it can offer one at all.
 */
function copilotOffersSessionApproval(request: Record<string, unknown> | undefined): boolean {
  if (!request)
    return false
  const offered = request.canOfferSessionApproval === true
  switch (pickString(request, 'kind')) {
    case 'read':
      return true
    case 'write':
      return offered
    case 'shell':
      return offered && Array.isArray(request.commands) && request.commands.length > 0
        && request.commands.every(command => isObject(command) && pickString(command, 'identifier') !== '')
    case 'mcp':
      return pickString(request, 'serverName') !== ''
    default:
      return false
  }
}

/**
 * The decision options for one Copilot permission request.
 *
 * Copilot's request carries no option list of its own, so LeapMux states the
 * decisions the runtime accepts for that request. The kinds are the shared
 * vocabulary the decision row lays out; the ids are what the answer carries back.
 */
export function copilotPermissionOptions(payload: Record<string, unknown>): WirePermissionOption[] {
  const request = copilotPermission(payload)
  const options: WirePermissionOption[] = [
    { optionId: COPILOT_ALLOW_ONCE, kind: KIND_ALLOW_ONCE, name: 'Allow once' },
  ]
  // The two wider scopes need the same approval rule, so a request that can express
  // one can express the other. The project scope outlives the session: the runtime
  // stores it against the working directory's own location key.
  if (copilotOffersSessionApproval(request)) {
    options.push({ optionId: COPILOT_ALLOW_SESSION, kind: KIND_ALLOW_ALWAYS, name: 'Allow for this session' })
    options.push({ optionId: COPILOT_ALLOW_PROJECT, kind: KIND_ALLOW_ALWAYS, name: 'Allow for this project' })
  }
  options.push({ optionId: COPILOT_REJECT, kind: KIND_REJECT_ONCE, name: 'Reject' })
  return options
}

/** Send one permission decision as the neutral envelope, with its approval scope. */
export function sendCopilotPermissionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
): Promise<void> {
  const response = COPILOT_ALLOW_SCOPES.has(optionId)
    ? { behavior: 'allow', scope: optionId }
    : { behavior: 'deny', message: CONTROL_REJECTED_BY_USER_MESSAGE }
  const envelope = { type: 'control_response', response: { subtype: 'success', request_id: requestId, response } }
  return onRespond(new TextEncoder().encode(JSON.stringify(envelope)))
}
