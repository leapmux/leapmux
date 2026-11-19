import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, controlBehaviorDisplay, controlDecisionWords, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'

/**
 * The display of one saved Antigravity answer.
 *
 * An answer in the neutral behavior envelope, which a plan review takes, reads through
 * the shared reader, with the words of the control that the recorded prompt states.
 * Any other answer is the language server's own interaction answer,
 * which the Worker built:
 *
 * - A permission states `allow`.
 * - A question states one response for each question, in the order that the request
 *   asked them: the selected option ids, the typed text, or `skipped`. A cancelled
 *   question states `cancelled`.
 *
 * The question lines take each question's text and option labels from the prompt that
 * the Worker recorded with the answer.
 */
export function antigravityControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const behavior = controlBehaviorDisplay(cr.response, controlDecisionWords(cr.prompt))
  if (behavior)
    return behavior
  const interaction = pickObject(cr.response, 'interaction')
  const permission = pickObject(interaction, 'permission')
  if (permission) {
    return label(permission.allow === true
      ? CONTROL_DECISION_WORDS.permission.allow
      : CONTROL_DECISION_WORDS.permission.deny)
  }
  const ask = pickObject(interaction, 'askQuestion')
  if (!ask)
    return null
  if (ask.cancelled === true)
    return label('Dismissed')
  const responses = Array.isArray(ask.responses)
    ? ask.responses
    : []
  const questions = cr.prompt?.kind === 'question'
    ? cr.prompt.questions
    : []
  const lines: string[] = []
  responses.forEach((answer, index) => {
    if (!isObject(answer))
      return
    const selected = Array.isArray(answer.selectedOptionIds)
      ? answer.selectedOptionIds.filter((id): id is string => typeof id === 'string')
      : []
    const labels = selected.map(id => questions[index]?.options.find(option => option.value === id)?.label ?? id)
    const text = pickString(answer, 'writeInResponse') || labels.join(', ') || (answer.skipped === true
      ? 'Skipped'
      : '')
    // `labeledAnswerLine` reads a list of values and reads anything else as none.
    const line = labeledAnswerLine(questions[index]?.question ?? `Question ${index + 1}`, [text])
    if (line !== null)
      lines.push(line)
  })
  return labelOrNull(joinAnswerLines(lines))
}
