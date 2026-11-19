import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { CONTROL_DECISION_WORDS, label } from '../../persistedControlResponse'
import { acpControlResponseSummary, acpOptionIdKind, acpPermissionOptions } from '../acp/controlResponse'

/**
 * Display the reader's plan decision, not the native option's generic label.
 *
 * Reasonix raises its plan gate as a permission request, and the prompt that the
 * Worker recorded with the answer states that it is a plan. The kind of the option
 * that the Worker sent states the decision: the request's own option, else the kind
 * that a well-known id states, as Reasonix's own ids are the protocol's kind tokens.
 */
export function reasonixControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  const optionId = pickString(pickObject(result, 'outcome'), 'optionId')
  if (result && cr.prompt?.kind === 'plan') {
    const kind = acpPermissionOptions(cr.request).find(option => option.optionId === optionId)?.kind ?? acpOptionIdKind(optionId)
    if (kind === ACP_PERMISSION_KIND.AllowOnce)
      return label(CONTROL_DECISION_WORDS.plan.allow)
    if (kind === KIND_REJECT_ONCE)
      return label(CONTROL_DECISION_WORDS.plan.deny)
  }
  return acpControlResponseSummary(cr)
}
