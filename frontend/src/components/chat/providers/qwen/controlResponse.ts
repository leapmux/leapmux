import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { pickObject, pickString } from '~/lib/jsonPick'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE } from '../../model/controlPrompt'
import { CONTROL_DECISION_WORDS, label, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseSummary, acpOptionIdKind } from '../acp/controlResponse'
import { acpPermissionOptions } from '../acp/extractControl'
import { isQwenQuestionPayload, qwenQuestionAnswerLines } from './askUserQuestion'
import { isQwenPlanApproval } from './extractControl'

/**
 * The display of one saved plan approval.
 *
 * The reader answered with Approve or Reject, and the worker turned that into one of
 * Qwen's four options. The row shows the words the reader's button carried, from the
 * KIND of the option the worker sent. The reason of a rejection travels as a message
 * of its own, which the transcript shows beside this row.
 */
function qwenPlanDisplay(request: Record<string, unknown>, optionId: string): ControlResponseSummary | null {
  const kind = acpPermissionOptions(request).find(option => option.optionId === optionId)?.kind ?? acpOptionIdKind(optionId)
  if (!kind)
    return null
  return label(kind === KIND_ALLOW_ONCE || kind === KIND_ALLOW_ALWAYS ? CONTROL_DECISION_WORDS.plan.allow : CONTROL_DECISION_WORDS.plan.deny)
}

/**
 * The display of one saved Qwen answer.
 *
 * Qwen raises its questions and its plan approval as permission requests, so the
 * dispatch reads the request. An answered question shows its answers; a dismissed
 * one shows the option that dismissed it, as every other permission does. A plan
 * option of no kind this build can read shows the option itself.
 *
 * Without the request, only the reply is left. Qwen's question reply is the one
 * reply that carries an answers field, so the answers still show, numbered, rather
 * than the kind of the submit option.
 */
export function qwenControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  const optionId = pickString(pickObject(result, 'outcome'), 'optionId')
  if (cr.request && result && isQwenPlanApproval(cr.request) && optionId) {
    const plan = qwenPlanDisplay(cr.request, optionId)
    if (plan !== null)
      return plan
  }
  if (result && (cr.request === undefined || isQwenQuestionPayload(cr.request))) {
    const answers = qwenQuestionAnswerLines(cr.request, result)
    if (answers !== null)
      return labelOrNull(answers)
  }
  return acpControlResponseSummary(cr)
}
