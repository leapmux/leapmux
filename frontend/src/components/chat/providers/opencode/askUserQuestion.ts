import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'

import { OPENCODE_ANSWER_FIELD } from '~/generated/contracts/opencode-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { questionsFromWire, sendResponse } from '../../controls/types'

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
 * Read the `properties.questions` array off an OpenCode-style `question.asked`
 * payload and normalize the legacy `multiple` field to `multiSelect`. Used by
 * both the OpenCode and Kilo plugins (Kilo is an OpenCode fork that shares
 * the same wire format).
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

export function sendOpenCodeQuestionResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  questions: ControlQuestion[],
  answerState: ControlAnswerState,
): Promise<void> {
  const answers: string[][] = questions.map((_, index) => {
    const selected = answerState.selections()[index] ?? []
    const customText = answerState.customTexts()[index]?.trim()
    if (selected.length > 0)
      return selected
    if (customText)
      return [customText]
    return []
  })
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: requestId,
    result: { [OPENCODE_ANSWER_FIELD.Answers]: answers },
  })
}

export function sendOpenCodeQuestionRejectResponse(
  onRespond: ControlResponseSender,
  requestId: string,
): Promise<void> {
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: requestId,
    result: { [OPENCODE_ANSWER_FIELD.Rejected]: true },
  })
}
