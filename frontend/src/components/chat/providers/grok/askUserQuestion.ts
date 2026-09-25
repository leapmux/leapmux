import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { GROK_METHOD, GROK_QUESTION_OUTCOME, GROK_REPLY_FIELD } from '~/generated/contracts/grok-protocol'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { questionOptionValue, questionsFromWire, sendJsonRpcResult } from '../../controls/types'
import { joinAnswerLines, labeledAnswerLine } from '../../persistedControlResponse'

/**
 * The label Grok reads as "the reader typed an answer of their own".
 *
 * Grok's reply states a free-text answer as this one label, with the text in the
 * notes of the same question. Only the browser writes it, so it is not in the contract.
 */
const GROK_FREE_TEXT_LABEL = 'Other'

/** Whether one control request is Grok's question dialog. */
export function isGrokQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.method === GROK_METHOD.AskUserQuestion
}

/**
 * The questions of one `_x.ai/ask_user_question`.
 *
 * Grok states `multiSelect` as `null` when the model did not set it, which the
 * shared reader would keep as a value that is not a boolean.
 */
export function grokQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const params = pickObject(payload, 'params')
  return questionsFromWire(params?.questions).map(question => ({ ...question, multiSelect: question.multiSelect === true }))
}

/** One question's annotation: the preview of the option it chose, and the reader's notes. */
interface GrokAnnotation {
  preview?: string
  notes?: string
}

/**
 * The reply to one question dialog, in Grok's own shape.
 *
 * Grok keys each answer by the question's TEXT and states it as the list of labels
 * the reader chose. A question the reader left empty is omitted. The reader's own
 * words go in the notes, beside the options they chose; with no option chosen, the
 * answer is the free-text label alone. A single-select option that carries a preview
 * sends it back, which is what Grok shows the model beside the choice.
 */
export function grokQuestionReply(questions: ControlQuestion[], answerState: ControlAnswerState): Record<string, unknown> {
  // Entries, then `Object.fromEntries`: the keys are the model's own question text,
  // and an assignment would set the prototype for a question worded `__proto__`
  // rather than add the answer.
  const answers: Array<[string, string[]]> = []
  const annotations: Array<[string, GrokAnnotation]> = []
  questions.forEach((question, index) => {
    const selected = answerState.selections()[index] ?? []
    const notes = answerState.customTexts()[index]?.trim() ?? ''
    if (selected.length === 0 && !notes)
      return
    answers.push([question.question, selected.length > 0 ? selected : [GROK_FREE_TEXT_LABEL]])
    const chosen = !question.multiSelect && selected.length === 1
      ? question.options.find(option => questionOptionValue(option) === selected[0])
      : undefined
    const annotation: GrokAnnotation = {
      ...(chosen?.preview ? { preview: chosen.preview } : {}),
      ...(notes ? { notes } : {}),
    }
    if (Object.keys(annotation).length > 0)
      annotations.push([question.question, annotation])
  })
  return {
    [GROK_REPLY_FIELD.Outcome]: GROK_QUESTION_OUTCOME.Accepted,
    answers: Object.fromEntries(answers),
    ...(annotations.length > 0 ? { annotations: Object.fromEntries(annotations) } : {}),
  }
}

export function sendGrokQuestionResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  questions: ControlQuestion[],
  answerState: ControlAnswerState,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, grokQuestionReply(questions, answerState))
}

/**
 * The reply that dismisses the dialog. Grok's `cancelled` carries no reason: Grok
 * tells the model that the reader declined to answer.
 */
export function sendGrokQuestionRejectResponse(onRespond: ControlResponseSender, requestId: string): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { [GROK_REPLY_FIELD.Outcome]: GROK_QUESTION_OUTCOME.Cancelled })
}

/**
 * The saved answer of one question dialog, one line for each question it answered,
 * in the order the dialog asked them.
 *
 * The free-text label is not an answer of its own: the notes beside it are. It
 * stays when the question itself offers an option with that label.
 */
export function grokQuestionAnswerLines(request: Record<string, unknown> | undefined, result: Record<string, unknown>): string | null {
  const answers = pickObject(result, 'answers')
  if (!answers)
    return null
  const annotations = pickObject(result, 'annotations')
  const questions = request ? grokQuestions(request) : []
  const order = questions.length > 0 ? questions.map(question => question.question) : Object.keys(answers)
  const lines = order.flatMap((text) => {
    const question = questions.find(candidate => candidate.question === text)
    const offersFreeTextLabel = question?.options.some(option => questionOptionValue(option) === GROK_FREE_TEXT_LABEL) ?? false
    const annotation = annotations && Object.hasOwn(annotations, text) ? annotations[text] : undefined
    const notes = pickString(isObject(annotation) ? annotation : undefined, 'notes').trim()
    const labels = stringArray(Object.hasOwn(answers, text) ? answers[text] : undefined).filter(label => offersFreeTextLabel || label !== GROK_FREE_TEXT_LABEL)
    const line = labeledAnswerLine(text, notes ? [...labels, notes] : labels)
    return line !== null ? [line] : []
  })
  return joinAnswerLines(lines)
}
