import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { OPENCODE_ANSWER_FIELD } from '~/generated/contracts/opencode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { CONTROL_DECISION_WORDS, controlBehaviorDisplay, controlDecisionWords, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'

/**
 * The words of the chosen permission option, from the options of the recorded prompt.
 *
 * The Worker offers MiMo's own reply words as the option ids (`mimoPermissionOptions`
 * in Go) and selects the chosen one as it is, so the saved id reads back as the option
 * the reader pressed. A selection that the prompt does not offer reads as nothing. A
 * result with no selection is MiMo's refusal.
 */
function permissionSummary(result: Record<string, unknown>, options: readonly PermissionOption[]): ControlResponseSummary | null {
  const outcome = pickObject(result, 'outcome')
  if (pickString(outcome, 'outcome') !== 'selected')
    return label(CONTROL_DECISION_WORDS.permission.deny)
  const optionId = pickString(outcome, 'optionId')
  const option = optionId
    ? options.find(candidate => candidate.optionId === optionId)
    : undefined
  return option
    ? label(permissionOptionLabel(option))
    : null
}

/**
 * The answer lines of one question request, labeled by each question's header.
 *
 * The Worker answers one list for each question of the recorded prompt, in its order
 * (`providerkit.AnswerOpenCodeQuestions` in Go), so a list's position is its
 * question's.
 */
function questionAnswerText(questions: readonly ControlQuestion[], result: Record<string, unknown>): string | null {
  if (result[OPENCODE_ANSWER_FIELD.Rejected] === true)
    return 'Dismissed'
  const rawAnswers = result[OPENCODE_ANSWER_FIELD.Answers]
  const answers: unknown[] = Array.isArray(rawAnswers)
    ? rawAnswers
    : []
  const lines: string[] = []
  answers.forEach((answer, index) => {
    const question = questions[index]
    const heading = question?.header || question?.question
    const line = labeledAnswerLine(heading || `Question ${index + 1}`, answer)
    if (line !== null)
      lines.push(line)
  })
  return joinAnswerLines(lines)
}

/**
 * The display of one saved MiMo answer.
 *
 * The answer keeps the shape that the Worker sends to MiMo. Three shapes reach here:
 *
 *   - The shared allow/deny envelope, which answers a plan and a permission. The
 *     envelope does not state which control it answers, so the words come from the
 *     prompt that the Worker recorded with the answer.
 *   - A chosen permission option.
 *   - The answers to a question.
 *
 * The recorded prompt states which request a result answers. A row that records no
 * prompt reads the envelope alone, and the caller falls back to the shared label for
 * anything else.
 */
export function mimoControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const behavior = controlBehaviorDisplay(cr.response, controlDecisionWords(cr.prompt))
  if (behavior)
    return behavior
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  switch (cr.prompt?.kind) {
    case 'permission':
      return permissionSummary(result, cr.prompt.permission.options)
    case 'plan':
      return result[OPENCODE_ANSWER_FIELD.Rejected] === true
        ? label(CONTROL_DECISION_WORDS.plan.deny)
        : null
    case 'question':
      return labelOrNull(questionAnswerText(cr.prompt.questions, result))
    default:
      return null
  }
}
