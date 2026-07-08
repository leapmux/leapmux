import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { feedbackOrLabel, firstNonEmpty, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseDisplay } from '../acp/controlResponse'

const CURSOR_METHOD_ASK_QUESTION = 'cursor/ask_question'
const CURSOR_METHOD_CREATE_PLAN = 'cursor/create_plan'

/** The transformed/native outcome object at `result.outcome`, or undefined when absent. */
function cursorOutcome(response: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  return pickObject(pickObject(response, 'result', undefined), 'outcome', undefined)
}

/**
 * The answered branch of the Cursor ask_question derivation: map each selected optionId to its
 * option LABEL (unknown ids pass through) and render "prompt: label1, label2" lines in
 * request-question order, dropping questions with no surviving selection. Null when nothing renders.
 */
function cursorAnswerLines(
  request: Record<string, unknown> | undefined,
  outcome: Record<string, unknown>,
): string | null {
  const params = pickObject(request, 'params', undefined)
  const questions = Array.isArray(params?.questions) ? params.questions : []

  const promptById = new Map<string, string>()
  const optionLabelById = new Map<string, Map<string, string>>()
  const order: string[] = []
  for (const q of questions) {
    if (!isObject(q))
      continue
    const id = pickString(q, 'id', '').trim()
    if (!id)
      continue
    promptById.set(id, pickString(q, 'prompt', '').trim())
    const optionLabels = new Map<string, string>()
    const options = Array.isArray(q.options) ? q.options : []
    for (const opt of options) {
      if (!isObject(opt))
        continue
      const optId = pickString(opt, 'id', '').trim()
      if (!optId)
        continue
      optionLabels.set(optId, firstNonEmpty(pickString(opt, 'label', ''), optId))
    }
    optionLabelById.set(id, optionLabels)
    order.push(id)
  }

  const answers = Array.isArray(outcome.answers) ? outcome.answers : []
  const answerByQuestion = new Map<string, string[]>()
  for (const answer of answers) {
    if (!isObject(answer))
      continue
    const questionId = pickString(answer, 'questionId', '').trim()
    if (!questionId)
      continue
    const mapped: string[] = []
    for (const raw of stringArray(answer.selectedOptionIds)) {
      const optId = raw.trim()
      if (!optId)
        continue
      const optionLabel = optionLabelById.get(questionId)?.get(optId)
      mapped.push(optionLabel && optionLabel.trim() ? optionLabel.trim() : optId)
    }
    if (mapped.length > 0)
      answerByQuestion.set(questionId, mapped)
  }

  const lines: string[] = []
  for (const questionId of order) {
    const mapped = answerByQuestion.get(questionId)
    if (!mapped || mapped.length === 0)
      continue
    const line = labeledAnswerLine(firstNonEmpty(promptById.get(questionId), questionId), mapped)
    if (line !== null)
      lines.push(line)
  }
  return joinAnswerLines(lines)
}

/** Derive the display for a Cursor ask_question answer (answered lines, or a cancellation reason). */
function cursorQuestionDisplay(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown> | undefined,
): ControlResponseDisplay | null {
  const outcome = cursorOutcome(response)
  if (!outcome)
    return null
  switch (pickString(outcome, 'outcome', '')) {
    case 'answered':
      return labelOrNull(cursorAnswerLines(request, outcome))
    case 'cancelled':
    case 'skipped':
      return feedbackOrLabel(pickString(outcome, 'reason', '').trim(), 'Cancel')
    default:
      return null
  }
}

/**
 * Derive the plan decision from the TRANSFORMED outcome the agent was sent
 * (`result.outcome.{outcome, reason}`): accepted -> "Accept"; rejected/cancelled -> the reason as
 * feedback, or "Reject"/"Cancel" when bare.
 */
function cursorCreatePlanDisplay(response: Record<string, unknown> | undefined): ControlResponseDisplay | null {
  const outcome = cursorOutcome(response)
  if (!outcome)
    return null
  const reason = pickString(outcome, 'reason', '').trim()
  switch (pickString(outcome, 'outcome', '')) {
    case 'accepted':
      return label('Accept')
    case 'rejected':
      return feedbackOrLabel(reason, 'Reject')
    case 'cancelled':
      return feedbackOrLabel(reason, 'Cancel')
    default:
      return null
  }
}

/**
 * Cursor control-response derivation: dispatch on the request method (ask_question / create_plan),
 * falling back to the shared ACP permission path for a plain permission selection. When the pruned
 * request is gone (method absent), a create_plan decision and a permission selection are still fully
 * recoverable from `result.outcome` alone (the plan verdict / the selected optionId live in the
 * response), so both are tried before the generic fallback. An ask_question ANSWERED outcome is the
 * one shape NOT fully recoverable request-gone: the selected option ids are in the response but their
 * human labels and the question prompts live only in the pruned request, so cursorAnswerLines yields
 * nothing and the row degrades to the neutral "Responded" -- an accepted limitation (the raw option
 * ids would be opaque tokens), not a recovery we can complete here.
 */
export function cursorControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  switch (pickString(cr.request, 'method', '')) {
    case CURSOR_METHOD_ASK_QUESTION:
      return cursorQuestionDisplay(cr.request, cr.response)
    case CURSOR_METHOD_CREATE_PLAN:
      return cursorCreatePlanDisplay(cr.response)
    case '':
      return cursorCreatePlanDisplay(cr.response)
        ?? cursorQuestionDisplay(cr.request, cr.response)
        ?? acpControlResponseDisplay(cr)
    default:
      return acpControlResponseDisplay(cr)
  }
}
