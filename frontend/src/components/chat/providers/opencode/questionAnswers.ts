import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { firstNonEmpty, joinAnswerLines, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseDisplay } from '../acp/controlResponse'

const OPENCODE_QUESTION_TYPE = 'question.asked'

/**
 * Render an OpenCode/Kilo `question.asked` answer. A rejected answer is "Reject"; otherwise each
 * answer group renders as a "Header: v1, v2" line labeled by its request question (header else
 * question, else "Question N"), with empty answer values dropped. Null when nothing renders.
 */
export function opencodeQuestionAnswersText(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  if (result?.rejected === true)
    return 'Reject'

  const answers = Array.isArray(result?.answers) ? result.answers : []
  const properties = pickObject(request, 'properties', undefined)
  const questions = Array.isArray(properties?.questions) ? properties.questions : []

  const lines: string[] = []
  for (let i = 0; i < answers.length; i++) {
    let label = `Question ${i + 1}`
    const q = questions[i]
    if (isObject(q)) {
      const named = firstNonEmpty(pickString(q, 'header', ''), pickString(q, 'question', ''))
      if (named)
        label = named
    }
    const line = labeledAnswerLine(label, answers[i])
    if (line !== null)
      lines.push(line)
  }
  return joinAnswerLines(lines)
}

/**
 * True when the response alone identifies an OpenCode/Kilo `question.asked` answer -- a rejection
 * flag or an `answers` array (a permission selection has neither; it carries `result.outcome`). Lets
 * a request-gone question answer still render its lines (positionally labeled) instead of degrading.
 */
function isOpencodeQuestionResponse(response: Record<string, unknown> | undefined): boolean {
  const result = pickObject(response, 'result', undefined)
  return result?.rejected === true || Array.isArray(result?.answers)
}

/**
 * OpenCode/Kilo control-response derivation: a `question.asked` answer renders its question lines;
 * anything else (a permission selection) delegates to the shared ACP path. When the pruned request
 * type is gone, the response shape still identifies a question answer, so it is recovered there --
 * labeled by position rather than the missing question headers.
 */
export function opencodeControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  if (pickString(cr.request, 'type', '') === OPENCODE_QUESTION_TYPE || isOpencodeQuestionResponse(cr.response))
    return labelOrNull(opencodeQuestionAnswersText(cr.request, cr.response))
  return acpControlResponseDisplay(cr)
}
