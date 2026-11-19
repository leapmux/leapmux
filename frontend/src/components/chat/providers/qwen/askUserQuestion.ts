import type { ControlQuestion } from '../../model/question'
import { QWEN_META, QWEN_REPLY_FIELD } from '~/generated/contracts/qwen-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { joinAnswerLines, labeledAnswerLine } from '../../persistedControlResponse'
import { acpPermissionToolCall } from '../acp/controlResponse'
import { questionsFromWire } from '../questionRecords'

/**
 * The questions of one question dialog.
 *
 * Qwen raises its questions as a standard permission request, and states them in the
 * tool call's `_meta`, and again as the tool call's own arguments. The `_meta` copy is
 * the one it marks for a client, so it wins.
 */
function qwenQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const toolCall = acpPermissionToolCall(payload)
  const meta = pickObject(toolCall, '_meta')
  const raw = Array.isArray(meta?.[QWEN_META.Questions])
    ? meta[QWEN_META.Questions]
    : pickObject(toolCall, 'rawInput')?.questions
  return questionsFromWire(raw)
}

/**
 * The saved answer of one question dialog, one line for each question it answered,
 * in the order the dialog asked them. Null when the reply states no answers, which
 * is the reply that dismissed the dialog.
 */
export function qwenQuestionAnswerLines(request: Record<string, unknown> | undefined, result: Record<string, unknown>): string | null {
  const answers = result[QWEN_REPLY_FIELD.Answers]
  if (!isObject(answers))
    return null
  const questions = request
    ? qwenQuestions(request)
    : []
  const indexes = questions.length > 0
    ? questions.map((_, index) => String(index))
    : Object.keys(answers)
  const lines = indexes.flatMap((index) => {
    const question = questions[Number(index)]
    const label = question?.header || question?.question || `Question ${Number(index) + 1}`
    const line = labeledAnswerLine(label, Object.hasOwn(answers, index)
      ? [answers[index]]
      : [])
    return line !== null
      ? [line]
      : []
  })
  return joinAnswerLines(lines)
}
