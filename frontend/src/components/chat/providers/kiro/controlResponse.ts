import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { KIRO_CONSENT_SCOPE, KIRO_META, KIRO_PERMISSION_OPTION, KIRO_SCOPED_PERMISSION_OPTION, KIRO_USER_INPUT_ACTION } from '~/generated/contracts/kiro-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { feedback, label, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseSummary } from '../acp/controlResponse'
import { kiroUserInputSavedAnswer } from './askUserQuestion'
import { kiroMeta } from './protocol'

/**
 * The option of LeapMux's own that each consent scope of a saved reply chose, for
 * Kiro's own always-allow and always-deny.
 *
 * Kiro states one `always-accept` and one `always-reject`, which keep their rule for
 * the session, and it reads a wider scope from the reply. The Worker offers each wider
 * scope as an option of its own (`kiroAlwaysAllowRules` and `kiroAlwaysDenyRules` in
 * Go), and it answers one with Kiro's own option and the scope in
 * `_meta.kiro.consent`. This reads such a reply back to the option of the recorded
 * prompt that the reader pressed. A `Map`, because the option id and the scope are
 * wire data, and a bare index on a record would reach `Object.prototype`.
 */
const KIRO_SCOPED_OPTION_IDS: ReadonlyMap<string, ReadonlyMap<string, string>> = new Map([
  [
    KIRO_PERMISSION_OPTION.AlwaysAccept,
    new Map<string, string>([
      [KIRO_CONSENT_SCOPE.Workspace, KIRO_SCOPED_PERMISSION_OPTION.AlwaysAcceptWorkspace],
      [KIRO_CONSENT_SCOPE.User, KIRO_SCOPED_PERMISSION_OPTION.AlwaysAcceptUser],
    ]),
  ],
  [
    KIRO_PERMISSION_OPTION.AlwaysReject,
    new Map<string, string>([
      [KIRO_CONSENT_SCOPE.Workspace, KIRO_SCOPED_PERMISSION_OPTION.AlwaysRejectWorkspace],
      [KIRO_CONSENT_SCOPE.User, KIRO_SCOPED_PERMISSION_OPTION.AlwaysRejectUser],
    ]),
  ],
])

/**
 * The id of the prompt's option that a saved reply chose, or '' for a reply that
 * selects no option.
 *
 * The reply can state Kiro's own always option with a wider scope, or the option id
 * itself: both read as the option the reader pressed.
 */
function kiroChosenOptionId(result: Record<string, unknown>): string {
  const optionId = pickString(pickObject(result, 'outcome'), 'optionId')
  const scope = pickString(pickObject(kiroMeta(result), KIRO_META.Consent), KIRO_META.Scope)
  return KIRO_SCOPED_OPTION_IDS.get(optionId)?.get(scope) ?? optionId
}

/**
 * The display of one saved permission: the reason that a rejection carried, which is
 * what the reader typed, or the words of the option of the recorded prompt that the
 * reader chose. A reply that selects no option takes the shared permission display,
 * which reads a cancellation. An option that the prompt does not offer reads as
 * nothing.
 */
function kiroPermissionSummary(cr: PersistedControlResponse, result: Record<string, unknown>, options: readonly PermissionOption[]): ControlResponseSummary | null {
  const reason = pickString(kiroMeta(result), KIRO_META.RejectionReason).trim()
  if (reason)
    return feedback(reason)
  const optionId = kiroChosenOptionId(result)
  if (!optionId)
    return acpControlResponseSummary(cr)
  const option = options.find(candidate => candidate.optionId === optionId)
  return option
    ? label(permissionOptionLabel(option))
    : null
}

/**
 * The display of one saved Kiro answer. The prompt that the Worker recorded with the
 * answer states which request it answers: the question takes Kiro's own reply, and a
 * permission takes the option that the reader chose.
 *
 * The MCP form takes the shared form display, which the row extraction asks before
 * this one, from the prompt that the Worker recorded. A row that records no prompt
 * reads as nothing, so the caller falls back to the shared label.
 */
export function kiroControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  switch (cr.prompt?.kind) {
    case 'question':
      if (pickString(result, 'action') === KIRO_USER_INPUT_ACTION.Dismissed)
        return label('Dismissed')
      return labelOrNull(kiroUserInputSavedAnswer(result))
    case 'permission':
      return kiroPermissionSummary(cr, result, cr.prompt.permission.options)
    default:
      return null
  }
}
