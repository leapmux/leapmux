import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { ControlAnswerBehavior } from '~/generated/contracts/control-answer'
import { CONTROL_ANSWER_BEHAVIOR } from '~/generated/contracts/control-answer'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { questionOptionValue } from '../../controls/types'
import { isAllowPermissionKind, isRejectPermissionKind } from '../../model/controlPrompt'
import { CONTROL_DECISION_WORDS, feedbackOrLabel, joinAnswerLines, label } from '../../persistedControlResponse'

/**
 * Read the exact saved native choices, by the prompt that the Worker recorded with them.
 *
 * - A permission reads the prompt's option of the decision: its allow or its refusal.
 * - A plan review reads the plan words, or the typed reason of a refusal.
 * - A question reads each answer through the question's own ID. A denial reads the
 *   decision alone, with or without a typed reason.
 *
 * The native approval reply carries no reason. The Worker sends the reason as the
 * reader's next message, and the transcript draws that message as a separate row.
 */
export function deepseekHarnessControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const decision = decodeControlBehaviorEnvelope(cr.response)
  if (!decision || decision.requestId !== cr.requestId)
    return null
  switch (cr.prompt?.kind) {
    case 'permission':
      return label(decisionLabel(cr.prompt.permission.options, decision.behavior))
    case 'plan':
      return decision.behavior === CONTROL_ANSWER_BEHAVIOR.Allow
        ? label(CONTROL_DECISION_WORDS.plan.allow)
        : feedbackOrLabel(decision.message, 'Keep planning')
    case 'question':
      return decision.behavior === CONTROL_ANSWER_BEHAVIOR.Deny
        ? feedbackOrLabel(decision.message, 'Dismissed')
        : answersSummary(cr.prompt.questions, cr.response)
    default:
      return null
  }
}

/** The label of the prompt's first option of the decision, or the shared word when it offers none. */
function decisionLabel(options: readonly PermissionOption[], behavior: ControlAnswerBehavior): string {
  const option = options.find(candidate => behavior === CONTROL_ANSWER_BEHAVIOR.Allow
    ? isAllowPermissionKind(candidate.kind)
    : isRejectPermissionKind(candidate.kind))
  if (option)
    return permissionOptionLabel(option)
  return behavior === CONTROL_ANSWER_BEHAVIOR.Allow
    ? CONTROL_DECISION_WORDS.permission.allow
    : CONTROL_DECISION_WORDS.permission.deny
}

/**
 * One line for each answered question: its selected options, then its typed text.
 * Every native answer must address one question of the prompt by its ID, once, and
 * select only offered options; anything else is not this request's saved answer.
 */
function answersSummary(questions: readonly ControlQuestion[], response: Record<string, unknown> | undefined): ControlResponseSummary | null {
  const native = pickObject(pickObject(response, 'response'), 'response')
  if (!Array.isArray(native?.answers))
    return null
  const answers = native.answers.filter(isObject)
  if (answers.length !== native.answers.length)
    return null
  const ids = answers.map(answer => pickString(answer, 'id'))
  if (ids.some(id => !id) || new Set(ids).size !== ids.length)
    return null
  const lines: string[] = []
  for (const question of questions) {
    const answer = answers.find(answer => answer.id === question.id)
    if (!answer)
      return null
    const offered = question.options.map(questionOptionValue)
    const selected = stringArray(answer.selected)
    if (selected.some(value => !offered.includes(value)))
      return null
    const custom = pickString(answer, 'custom')
    const text = [
      ...selected,
      ...(custom
        ? [custom]
        : []),
    ].join(', ')
    if (text)
      lines.push(`${question.question || question.header || question.id}: ${text}`)
  }
  const text = joinAnswerLines(lines)
  return text
    ? label(text)
    : label('No answer')
}
