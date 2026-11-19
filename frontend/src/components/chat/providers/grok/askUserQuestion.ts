import type { ControlQuestion } from '../../model/question'
import { GROK_ANSWER_LABEL } from '~/generated/contracts/grok-protocol'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { questionOptionValue } from '../../controls/types'
import { joinAnswerLines, labeledAnswerLine } from '../../persistedControlResponse'

/**
 * The saved answer of one question dialog, one line for each question it answered,
 * in the order the dialog asked them: the questions of the prompt that the Worker
 * recorded with the answer. With no question, the answers state their own order.
 *
 * The free-text label is not an answer of its own: the notes beside it are. It
 * stays when the question itself offers an option with that label.
 */
export function grokQuestionAnswerLines(questions: readonly ControlQuestion[], result: Record<string, unknown>): string | null {
  const answers = pickObject(result, 'answers')
  if (!answers)
    return null
  const annotations = pickObject(result, 'annotations')
  const order = questions.length > 0
    ? questions.map(question => question.question)
    : Object.keys(answers)
  const lines = order.flatMap((text) => {
    const question = questions.find(candidate => candidate.question === text)
    const offersFreeTextLabel = question?.options.some(option => questionOptionValue(option) === GROK_ANSWER_LABEL.FreeText) ?? false
    const annotation = annotations && Object.hasOwn(annotations, text)
      ? annotations[text]
      : undefined
    const notes = pickString(isObject(annotation)
      ? annotation
      : undefined, 'notes').trim()
    const labels = stringArray(Object.hasOwn(answers, text)
      ? answers[text]
      : undefined).filter(label => offersFreeTextLabel || label !== GROK_ANSWER_LABEL.FreeText)
    const line = labeledAnswerLine(text, notes
      ? [...labels, notes]
      : labels)
    return line !== null
      ? [line]
      : []
  })
  return joinAnswerLines(lines)
}
