import type { ControlAnswerState, ControlResponseSender } from '../controls/types'
/**
 * Native questions shared by Kilo and MiMo Code.
 * Events carry questions under properties.questions. Each answer carries one
 * list of selected labels per question, or a rejection. The native-questions
 * contract supplies the event and answer vocabulary for both readers.
 */

import type { ControlQuestion } from '../model/question'
import { NATIVE_QUESTION_ANSWER_FIELD } from '~/generated/contracts/native-questions'
import { isObject, pickObject } from '~/lib/jsonPick'
import { questionsFromWire, sendJsonRpcResult } from '../controls/types'

/**
 * Fold the native `multiple` field onto `multiSelect`, on the RAW element.
 *
 * Before the shared reader types the element, not after: `ControlQuestion` declares
 * `multiSelect` and carries no `multiple`, so a fold afterwards has to assert its way
 * back into the untyped record it just left. An element that states `multiSelect`
 * already keeps it, and a `multiple` that is not a boolean states nothing.
 */
function foldMultiple(raw: unknown): unknown {
  if (!Array.isArray(raw))
    return raw
  return raw.map(entry => isObject(entry) && entry.multiSelect === undefined && typeof entry.multiple === 'boolean'
    ? { ...entry, multiSelect: entry.multiple }
    : entry)
}

/**
 * Read the `properties.questions` array of a `question.asked` payload, and fold the
 * native `multiple` field onto `multiSelect`.
 *
 * The shared reader, not a cast. The one upstream guard tests `payload.type`, which
 * says nothing about `properties`: a `questions` that held a string, a number or an
 * object threw `rawQuestions.map is not a function`, because `?? []` answers for
 * `null` and `undefined` alone. An array whose elements were bare strings spread one
 * character per key, and `AskUserQuestionControl` then dereferenced a `question` field
 * that no such element has and handed `options` to a `<For>`.
 */
export function extractNativeQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const properties = pickObject(payload, 'properties', undefined)
  return questionsFromWire(foldMultiple(properties?.questions))
}

/**
 * Answer one question request: for each question, in the question order, the options
 * the reader chose, else the words the reader typed, else no answer.
 */
export function sendNativeQuestionResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  questions: ControlQuestion[],
  answerState: ControlAnswerState,
): Promise<void> {
  const answers: string[][] = questions.map((_, index) => {
    const selected = answerState.selections()[index] ?? []
    if (selected.length > 0)
      return selected
    const customText = answerState.customTexts()[index]?.trim()
    return customText
      ? [customText]
      : []
  })
  return sendJsonRpcResult(onRespond, requestId, { [NATIVE_QUESTION_ANSWER_FIELD.Answers]: answers })
}

/** Reject one question request. */
export function sendNativeQuestionRejectResponse(
  onRespond: ControlResponseSender,
  requestId: string,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { [NATIVE_QUESTION_ANSWER_FIELD.Rejected]: true })
}
