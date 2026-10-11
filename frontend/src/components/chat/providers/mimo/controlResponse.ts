import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { MIMO_EVENT } from '~/generated/contracts/mimo-protocol'
import { OPENCODE_ANSWER_FIELD, OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, controlBehaviorDisplay, controlDecisionWords, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'
import { MIMO_PERMISSION_OPTIONS, mimoExtractControl } from './extractControl'

/** The words each permission option states once the reader chose it. */
function permissionOptionWords(optionId: string): string | null {
  return MIMO_PERMISSION_OPTIONS.find(option => option.optionId === optionId)?.name ?? null
}

/** The answer lines of one question request, labeled by each question's header. */
function questionAnswerText(request: Record<string, unknown> | undefined, result: Record<string, unknown>): string | null {
  if (result[OPENCODE_ANSWER_FIELD.Rejected] === true)
    return 'Dismissed'
  const rawAnswers = result[OPENCODE_ANSWER_FIELD.Answers]
  const answers: unknown[] = Array.isArray(rawAnswers) ? rawAnswers : []
  const rawQuestions = pickObject(request, 'properties')?.questions
  const questions: unknown[] = Array.isArray(rawQuestions) ? rawQuestions : []
  const lines: string[] = []
  answers.forEach((answer, index) => {
    const question = questions[index]
    const heading = isObject(question) ? pickString(question, 'header') || pickString(question, 'question') : ''
    const line = labeledAnswerLine(heading || `Question ${index + 1}`, answer)
    if (line !== null)
      lines.push(line)
  })
  return joinAnswerLines(lines)
}

/**
 * The display of one saved MiMo answer.
 *
 * The answer keeps the shape the browser wrote, which the worker also sends to MiMo.
 * Three shapes reach here:
 *
 *   - The shared allow/deny envelope, which answers a plan and a permission. The
 *     envelope does not state which control it answers, so the words come from the
 *     control that the banner drew for the request.
 *   - A chosen permission option.
 *   - The answers to a question.
 */
export function mimoControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const behavior = controlBehaviorDisplay(cr.response, controlDecisionWords(mimoExtractControl({ payload: cr.request ?? {} })))
  if (behavior)
    return behavior
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  const type = pickString(cr.request, 'type')
  const outcome = pickObject(result, 'outcome')
  if (type === MIMO_EVENT.PermissionAsked || outcome) {
    if (pickString(outcome, 'outcome') !== 'selected')
      return label(CONTROL_DECISION_WORDS.permission.deny)
    return labelOrNull(permissionOptionWords(pickString(outcome, 'optionId')))
  }
  if (type === OPENCODE_EVENT.QuestionAsked && mimoExtractControl({ payload: cr.request ?? {} })?.kind === 'plan')
    return result[OPENCODE_ANSWER_FIELD.Rejected] === true ? label(CONTROL_DECISION_WORDS.plan.deny) : null
  return labelOrNull(questionAnswerText(cr.request, result))
}
