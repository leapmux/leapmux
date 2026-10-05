import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { LETTA_QUESTION, LETTA_REPLY } from '~/generated/contracts/letta-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizeRejectionMessage } from '~/utils/controlResponse'
import { controlDecisionWords, feedbackOrLabel, joinAnswerLines, label, labeledAnswerLine } from '../../persistedControlResponse'
import { lettaQuestionsFromPayload } from './askUserQuestion'
import { lettaExtractControl } from './extractControl'

/**
 * The display of one saved Letta Code answer.
 *
 * The saved row holds the bytes that the worker sent to Letta Code, not the
 * neutral envelope that the browser sent. They take one of two forms:
 *
 * - A permission is the flat `approval_response` payload
 *   (`lettaResolveControlResponse`). Its `decision` states the behavior that
 *   Letta received.
 * - A question is the response that the worker wraps in a task notification. Its
 *   `status` says whether the reader answered or dismissed the questions, and its
 *   `answers` map holds one answer for each question, keyed by the question text.
 *
 * Null for a reply that is neither, so the caller falls back to the shared
 * display, which reads the neutral envelope or shows the generic label.
 */
export function lettaControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  if (pickString(cr.response, LETTA_QUESTION.FieldType) === LETTA_QUESTION.ResponseType)
    return questionResponseSummary(cr)
  if (pickString(cr.response, LETTA_REPLY.Kind) !== LETTA_REPLY.KindApprovalResponse)
    return null
  const decision = pickObject(cr.response, LETTA_REPLY.Decision)
  if (!decision)
    return null
  const words = controlDecisionWords(lettaExtractControl({ payload: cr.request ?? {} }))
  const behavior = pickString(decision, LETTA_REPLY.Behavior)
  if (behavior === 'deny')
    return feedbackOrLabel(normalizeRejectionMessage(pickString(decision, LETTA_REPLY.Message)), words.deny)
  if (behavior !== 'allow')
    return null
  return label(words.allow)
}

/**
 * The answer lines of a question response, in the order the stored request asked
 * its questions. The `answers` object is keyed by question text, so its stored
 * key order identifies nothing; the request's question order does.
 */
function questionResponseSummary(cr: PersistedControlResponse): ControlResponseSummary {
  if (pickString(cr.response, LETTA_QUESTION.FieldStatus) === LETTA_QUESTION.StatusDismissed)
    return label('Dismissed')
  const answers = pickObject(cr.response, LETTA_QUESTION.FieldAnswers)
  const lines = lettaQuestionsFromPayload(cr.request ?? {}).flatMap((question) => {
    const line = labeledAnswerLine(question.question, [pickString(answers, question.question)])
    return line ? [line] : []
  })
  return label(joinAnswerLines(lines) ?? 'No answer')
}
