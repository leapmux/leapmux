import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { OPENCODE_ANSWER_FIELD } from '~/generated/contracts/opencode-protocol'
import { pickObject } from '~/lib/jsonPick'
import { firstNonEmpty, joinAnswerLines, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseSummary } from '../acp/controlResponse'

/**
 * Render a Kilo `question.asked` answer. A rejected answer is "Reject"; otherwise each answer
 * group renders as a "Header: v1, v2" line labeled by the question of the recorded prompt at its
 * position (header else question, else "Question N"), with empty answer values dropped. Null when
 * nothing renders.
 *
 * The Worker answers one group for each question of the prompt, in its order
 * (`providerkit.AnswerOpenCodeQuestions` in Go), so a group's position is its question's.
 */
export function kiloQuestionAnswersText(
  questions: readonly ControlQuestion[],
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  if (result?.[OPENCODE_ANSWER_FIELD.Rejected] === true)
    return 'Reject'

  const groups = result?.[OPENCODE_ANSWER_FIELD.Answers]
  const answers = Array.isArray(groups)
    ? groups
    : []

  const lines: string[] = []
  for (let i = 0; i < answers.length; i++) {
    const question = questions[i]
    const label = firstNonEmpty(question?.header, question?.question) || `Question ${i + 1}`
    const line = labeledAnswerLine(label, answers[i])
    if (line !== null)
      lines.push(line)
  }
  return joinAnswerLines(lines)
}

/**
 * Kilo control-response derivation. The prompt that the Worker recorded with the answer states
 * which request it answers: a `question.asked` renders its question lines, and anything else (a
 * permission selection) delegates to the shared ACP path.
 */
export function kiloControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  if (cr.prompt?.kind === 'question')
    return labelOrNull(kiloQuestionAnswersText(cr.prompt.questions, cr.response))
  return acpControlResponseSummary(cr)
}
