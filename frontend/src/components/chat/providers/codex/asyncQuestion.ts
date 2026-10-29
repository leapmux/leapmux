import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { ControlRequest } from '~/stores/control.store'
import { CODEX_ASYNC_QUESTION_INPUT, CODEX_CONTROL_TOOL } from '~/generated/contracts/codex-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope, buildDenyResponse, decodeControlBehaviorEnvelope, getToolInput, getToolName } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { label } from '../../persistedControlResponse'

/**
 * Whether the payload is one of LeapMux's own async question requests: the
 * questions Codex asks WITHOUT waiting, which the worker published under its
 * own tool name.
 */
export function isCodexAsyncQuestion(payload: Record<string, unknown>): boolean {
  return getToolName(payload) === CODEX_CONTROL_TOOL.AsyncQuestion
}

/**
 * Read the one async question of a request.
 *
 * A malformed payload gives an empty list, as `questionsFromWire` does: the
 * control then has no question to answer, so Submit stays disabled and the
 * reader can only skip it.
 */
export function codexAsyncQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const input = getToolInput(payload)
  const title = pickString(input, CODEX_ASYNC_QUESTION_INPUT.Title, '')
  if (!title)
    return []
  const raw = input[CODEX_ASYNC_QUESTION_INPUT.Options]
  const options = Array.isArray(raw)
    ? raw.filter((entry): entry is string => typeof entry === 'string' && entry.trim() !== '')
    : []
  return [
    {
      question: title,
      options: options.map(option => ({ label: option })),
    },
  ]
}

/**
 * Answer one async question with the reader's selection, note, or both.
 *
 * Codex keeps no pending question state, so the whole answer travels in one
 * message. `preservesSelectionNotes` preserves both the selection and the note.
 * The answer includes both values in one message.
 */
export function sendCodexAsyncAnswer(
  request: ControlRequest,
  send: ControlResponseSender,
  answerState: ControlAnswerState,
): Promise<void> {
  const selection = answerState.selections()[0]?.[0] ?? ''
  const note = answerState.customTexts()[0]?.trim() ?? ''
  const answer = selection && note
    ? `${selection}\n\n${note}`
    : selection || note
  return sendResponse(send, buildControlResponseEnvelope(request.requestId, { behavior: 'allow', answer }))
}

/** Skip one async question. The worker sends nothing on to Codex. */
export function sendCodexAsyncSkip(request: ControlRequest, send: ControlResponseSender): Promise<void> {
  return sendResponse(send, buildDenyResponse(request.requestId, ''))
}

/**
 * The persisted display of one answered or skipped async question: the quoted
 * title with the answer, or the title under "Skipped". Any other request gives
 * null, so the caller uses the neutral behavior display.
 */
export function codexAsyncQuestionSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  if (!cr.request || !isCodexAsyncQuestion(cr.request))
    return null
  const title = pickString(getToolInput(cr.request), CODEX_ASYNC_QUESTION_INPUT.Title, '')
  // The stored row keeps the neutral envelope the browser sent, whose behavior
  // and answer live at its innermost `response` (`{response: {response: {...}}}`),
  // as Copilot's reader knows.
  const answer = pickObject(pickObject(cr.response, 'response'), 'response')
  const behavior = decodeControlBehaviorEnvelope(cr.response)?.behavior
  if (behavior === 'deny')
    return label(`Skipped: ${title}`)
  if (behavior === 'allow')
    return label(`${title}: ${pickString(answer, 'answer', '')}`)
  return null
}
