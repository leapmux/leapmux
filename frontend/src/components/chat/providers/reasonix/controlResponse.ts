import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { CONTROL_DECISION_WORDS, label } from '../../persistedControlResponse'
import { acpControlResponseSummary } from '../acp/controlResponse'
import { acpPermissionOptions } from '../acp/extractControl'
import { isReasonixPlanApproval } from './extractControl'

/** Display the reader's plan decision, not the native option's generic label. */
export function reasonixControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  const optionId = pickString(pickObject(result, 'outcome'), 'optionId')
  if (cr.request && result && isReasonixPlanApproval(cr.request)) {
    const kind = acpPermissionOptions(cr.request).find(option => option.optionId === optionId)?.kind
    if (kind === ACP_PERMISSION_KIND.AllowOnce)
      return label(CONTROL_DECISION_WORDS.plan.allow)
    if (kind === KIND_REJECT_ONCE)
      return label(CONTROL_DECISION_WORDS.plan.deny)
  }
  return acpControlResponseSummary(cr)
}
