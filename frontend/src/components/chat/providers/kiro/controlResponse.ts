import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { KiroAlwaysAllowID, KiroAlwaysDenyID } from './extractControl'
import { KIRO_CONSENT_SCOPE, KIRO_META, KIRO_METHOD, KIRO_PERMISSION_OPTION, KIRO_SCOPED_PERMISSION_OPTION, KIRO_USER_INPUT_ACTION } from '~/generated/contracts/kiro-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { feedback, label, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseSummary } from '../acp/controlResponse'
import { kiroUserInputSavedAnswer } from './askUserQuestion'
import { KIRO_ALWAYS_ALLOW, KIRO_ALWAYS_DENY } from './extractControl'
import { KIRO_REJECTION_REASON, kiroMeta } from './protocol'

/**
 * Adds the reason of a rejected permission to Kiro's reply, in `_meta.kiro`, and keeps
 * every other key the reply's metadata carried.
 */
export function kiroPermissionRejectReason(result: Record<string, unknown>, reason: string): Record<string, unknown> {
  const meta = pickObject(result, '_meta') ?? {}
  const kiro = pickObject(meta, KIRO_META.Namespace) ?? {}
  return { ...result, _meta: { ...meta, [KIRO_META.Namespace]: { ...kiro, [KIRO_REJECTION_REASON]: reason } } }
}

/** The option of each consent scope of a saved reply, for an always-allow and an always-deny. */
const KIRO_SCOPE_OPTIONS: Readonly<Record<string, { allow: KiroAlwaysAllowID, deny: KiroAlwaysDenyID }>> = {
  [KIRO_CONSENT_SCOPE.Workspace]: { allow: KIRO_SCOPED_PERMISSION_OPTION.AlwaysAcceptWorkspace, deny: KIRO_SCOPED_PERMISSION_OPTION.AlwaysRejectWorkspace },
  [KIRO_CONSENT_SCOPE.User]: { allow: KIRO_SCOPED_PERMISSION_OPTION.AlwaysAcceptUser, deny: KIRO_SCOPED_PERMISSION_OPTION.AlwaysRejectUser },
}

/** The option that the consent scope of a saved reply states, or undefined for the session. */
function kiroSavedScope(result: Record<string, unknown>): { allow: KiroAlwaysAllowID, deny: KiroAlwaysDenyID } | undefined {
  const scope = pickString(pickObject(kiroMeta(result), KIRO_META.Consent), KIRO_META.Scope)
  return Object.hasOwn(KIRO_SCOPE_OPTIONS, scope) ? KIRO_SCOPE_OPTIONS[scope] : undefined
}

/**
 * The words of one saved always-allow or always-deny, with its scope.
 *
 * The browser sends a wider scope as an option of LeapMux's own, and the worker turns
 * it into Kiro's own option with the scope in `_meta.kiro.consent`. A saved reply can
 * hold either form, and both read as the button the reader pressed.
 */
function kiroAlwaysRuleLabel(result: Record<string, unknown>): string | null {
  const optionId = pickString(pickObject(result, 'outcome'), 'optionId')
  if (optionId === KIRO_PERMISSION_OPTION.AlwaysAccept)
    return KIRO_ALWAYS_ALLOW[kiroSavedScope(result)?.allow ?? KIRO_PERMISSION_OPTION.AlwaysAccept].name
  if (optionId === KIRO_PERMISSION_OPTION.AlwaysReject)
    return KIRO_ALWAYS_DENY[kiroSavedScope(result)?.deny ?? KIRO_PERMISSION_OPTION.AlwaysReject].name
  if (Object.hasOwn(KIRO_ALWAYS_ALLOW, optionId))
    return KIRO_ALWAYS_ALLOW[optionId as KiroAlwaysAllowID].name
  if (Object.hasOwn(KIRO_ALWAYS_DENY, optionId))
    return KIRO_ALWAYS_DENY[optionId as KiroAlwaysDenyID].name
  return null
}

/**
 * The display of one saved Kiro answer. It dispatches on the request, and a request
 * the reader answered with one of its options takes the shared permission display.
 *
 * A rejection that carried a reason shows the reason, which is what the reader typed.
 * The MCP form takes the shared form display, which the registration wraps around this.
 */
export function kiroControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return acpControlResponseSummary(cr)
  if (pickString(cr.request, 'method') === KIRO_METHOD.UserInput) {
    if (pickString(result, 'action') === KIRO_USER_INPUT_ACTION.Dismissed)
      return label('Dismissed')
    return labelOrNull(kiroUserInputSavedAnswer(result))
  }
  const reason = pickString(kiroMeta(result), KIRO_REJECTION_REASON).trim()
  if (reason)
    return feedback(reason)
  return labelOrNull(kiroAlwaysRuleLabel(result)) ?? acpControlResponseSummary(cr)
}
