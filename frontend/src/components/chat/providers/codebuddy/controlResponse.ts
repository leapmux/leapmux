import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { CODEBUDDY_CONTROL_ANSWER_FIELD } from '~/generated/contracts/codebuddy-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizeRejectionMessage } from '~/utils/controlResponse'
import { controlDecisionWords, feedbackOrLabel, label } from '../../persistedControlResponse'
import { codebuddyExtractControl } from './extractControl'

/**
 * The saved answer of one CodeBuddy control request.
 *
 * The saved response is CodeBuddy's own can_use_tool answer, which the worker translated
 * from the browser's neutral envelope: `{response:{response:{allowed, reason}}}`. The
 * shared fallback reads only that neutral envelope, so without this reader every
 * CodeBuddy answer read "Responded" and a typed reason disappeared.
 *
 * An answer reads the words its own buttons carried: Allow and Deny for a permission,
 * Approve and Reject for a plan. A denial with the reader's own reason reads as the
 * feedback that reached the model. The reason that the worker fills in for a bare denial
 * is no reason of the reader's, so that denial reads the decision word alone.
 */
export function codebuddyControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const answer = pickObject(pickObject(cr.response, 'response', undefined), 'response', undefined)
  const allowed = answer?.[CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]
  if (typeof allowed !== 'boolean')
    return null
  const words = controlDecisionWords(codebuddyExtractControl({ payload: cr.request ?? {} }))
  if (allowed)
    return label(words.allow)
  return feedbackOrLabel(normalizeRejectionMessage(pickString(answer, CODEBUDDY_CONTROL_ANSWER_FIELD.Reason, '')), words.deny)
}
