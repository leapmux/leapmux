/**
 * The question wire of the OpenCode protocol.
 *
 * `question.asked` states its questions under `properties.questions`, and the answer
 * states one list of chosen labels for each question, or a rejection. The event and
 * the answer fields come from `contracts/opencode-protocol.json`, which the worker
 * reads too.
 *
 * Three providers speak this wire. OpenCode and Kilo, an OpenCode fork, reach it
 * through the Agent Client Protocol family (`registerOpenCodeProtocolProvider`). MiMo
 * Code, another OpenCode fork, reaches it over its own HTTP routes and stays outside
 * that family. So the helpers live here, beside the family and not inside it, and
 * each provider decides for itself which requests are questions.
 */

import type { ControlAnswerState, ControlResponseSender } from '../controls/types'
import type { ControlQuestion } from '../model/question'
import { OPENCODE_ANSWER_FIELD } from '~/generated/contracts/opencode-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { questionsFromWire, sendJsonRpcResult } from '../controls/types'

/**
 * Fold the legacy `multiple` field onto `multiSelect`, on the RAW element.
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
 * legacy `multiple` field onto `multiSelect`.
 *
 * The shared reader, not a cast. The one upstream guard tests `payload.type`, which
 * says nothing about `properties`: a `questions` that held a string, a number or an
 * object threw `rawQuestions.map is not a function`, because `?? []` answers for
 * `null` and `undefined` alone. An array whose elements were bare strings spread one
 * character per key, and `AskUserQuestionControl` then dereferenced a `question` field
 * that no such element has and handed `options` to a `<For>`.
 */
export function extractOpenCodeQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const properties = pickObject(payload, 'properties', undefined)
  return questionsFromWire(foldMultiple(properties?.questions))
}

/**
 * Answer one question request: for each question, in the question order, the options
 * the reader chose, else the words the reader typed, else no answer.
 */
export function sendOpenCodeQuestionResponse(
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
    return customText ? [customText] : []
  })
  return sendJsonRpcResult(onRespond, requestId, { [OPENCODE_ANSWER_FIELD.Answers]: answers })
}

/** Reject one question request. */
export function sendOpenCodeQuestionRejectResponse(
  onRespond: ControlResponseSender,
  requestId: string,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { [OPENCODE_ANSWER_FIELD.Rejected]: true })
}
