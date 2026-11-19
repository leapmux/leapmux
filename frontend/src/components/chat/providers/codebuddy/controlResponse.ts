import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { CODEBUDDY_CONTROL_ANSWER_FIELD } from '~/generated/contracts/codebuddy-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizeRejectionMessage } from '~/utils/controlResponse'
import { controlDecisionWords, feedbackOrLabel, label } from '../../persistedControlResponse'

/**
 * Read the saved answer of one CodeBuddy control request.
 *
 * The Worker stores CodeBuddy's native can_use_tool answer as
 * `{response:{response:{allowed, reason}}}`. The shared neutral reader expects
 * `behavior` and cannot decode this answer. This reader reads CodeBuddy's saved
 * fields to show the decision and refusal reason.
 *
 * Permission answers use Allow and Deny. Plan answers use Approve and Reject. The
 * prompt that the Worker recorded with the answer states which of the two the
 * request was. A denial with the reader's reason shows feedback. A bare denial shows
 * the decision word and omits the Worker's default reason.
 */
export function codebuddyControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const answer = pickObject(pickObject(cr.response, 'response', undefined), 'response', undefined)
  const allowed = answer?.[CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]
  if (typeof allowed !== 'boolean')
    return null
  const words = controlDecisionWords(cr.prompt)
  if (allowed)
    return label(words.allow)
  return feedbackOrLabel(normalizeRejectionMessage(pickString(answer, CODEBUDDY_CONTROL_ANSWER_FIELD.Reason, '')), words.deny)
}
