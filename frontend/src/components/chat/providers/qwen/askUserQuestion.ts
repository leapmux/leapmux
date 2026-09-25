import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { ACP_PERMISSION_OUTCOME } from '~/generated/contracts/acp-protocol'
import { QWEN_META, QWEN_PERMISSION_OPTION } from '~/generated/contracts/qwen-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { questionsFromWire, sendJsonRpcResult } from '../../controls/types'
import { KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { joinAnswerLines, labeledAnswerLine } from '../../persistedControlResponse'
import { acpPermissionOptions, acpPermissionToolCall } from '../acp/extractControl'

/**
 * The interaction kind Qwen states in a tool call's `_meta` for a question dialog.
 *
 * Only the browser reads it: the worker forwards a question and its reply unchanged,
 * so it is not in the contract.
 */
const QWEN_USER_QUESTION = 'user_question'

/**
 * The field of Qwen's permission reply that carries the answers of a question, keyed
 * by the question's index. Only the browser writes and reads it, for the reason
 * {@link QWEN_USER_QUESTION} states.
 */
const QWEN_ANSWERS_FIELD = 'answers'

/**
 * The separator Qwen's own dialog puts between the labels of a multi-select answer.
 *
 * Qwen reads each answer as one string, so the reply joins the labels the way Qwen's
 * dialog does. Only the browser writes it, so it is not in the contract.
 */
const QWEN_MULTI_SELECT_SEPARATOR = ', '

/** The `_meta` of the tool call one permission request is about. */
function qwenRequestMeta(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(acpPermissionToolCall(payload), '_meta') ?? undefined
}

/**
 * Whether one control request is Qwen's question dialog.
 *
 * Qwen raises its questions as a standard permission request, and it marks them in
 * the tool call's `_meta` with the interaction kind.
 */
export function isQwenQuestionPayload(payload: Record<string, unknown>): boolean {
  return pickString(qwenRequestMeta(payload), QWEN_META.InteractionKind) === QWEN_USER_QUESTION
}

/**
 * The questions of one question dialog.
 *
 * Qwen states them in `_meta`, and again as the tool call's own arguments. The
 * `_meta` copy is the one it marks for a client, so it wins.
 */
export function qwenQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const meta = qwenRequestMeta(payload)
  const raw = Array.isArray(meta?.[QWEN_META.Questions])
    ? meta[QWEN_META.Questions]
    : pickObject(acpPermissionToolCall(payload), 'rawInput')?.questions
  return questionsFromWire(raw).map(question => ({ ...question, multiSelect: question.multiSelect === true }))
}

/**
 * The option of one kind that the request offers, else Qwen's own id for it.
 *
 * Qwen answers a question through the permission options it sent beside it: its
 * `Submit` is an allow option, and its `Cancel` is a reject option.
 */
function qwenOptionOfKind(payload: Record<string, unknown>, kind: string, fallback: string): string {
  return acpPermissionOptions(payload).find(option => option.kind === kind && option.optionId !== '')?.optionId ?? fallback
}

/**
 * The reply to one question dialog, in Qwen's own shape.
 *
 * Qwen reads the SUBMIT option as the permission outcome, and the answers from a
 * field of its own beside it, keyed by the question's index. Each answer is one
 * string: the chosen labels joined as Qwen's dialog joins them, or the reader's own
 * words. A question the reader left empty is omitted.
 */
export function qwenQuestionReply(payload: Record<string, unknown>, questions: ControlQuestion[], answerState: ControlAnswerState): Record<string, unknown> {
  const answers: Record<string, string> = {}
  questions.forEach((_, index) => {
    const selected = answerState.selections()[index] ?? []
    const typed = answerState.customTexts()[index]?.trim() ?? ''
    const answer = selected.length > 0 ? selected.join(QWEN_MULTI_SELECT_SEPARATOR) : typed
    if (answer)
      answers[String(index)] = answer
  })
  return {
    outcome: { outcome: ACP_PERMISSION_OUTCOME.Selected, optionId: qwenOptionOfKind(payload, KIND_ALLOW_ONCE, QWEN_PERMISSION_OPTION.ProceedOnce) },
    [QWEN_ANSWERS_FIELD]: answers,
  }
}

export function sendQwenQuestionResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  payload: Record<string, unknown>,
  questions: ControlQuestion[],
  answerState: ControlAnswerState,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, qwenQuestionReply(payload, questions, answerState))
}

/** The reply that dismisses the dialog: Qwen's own cancel option. */
export function sendQwenQuestionRejectResponse(onRespond: ControlResponseSender, requestId: string, payload: Record<string, unknown>): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, {
    outcome: { outcome: ACP_PERMISSION_OUTCOME.Selected, optionId: qwenOptionOfKind(payload, KIND_REJECT_ONCE, QWEN_PERMISSION_OPTION.Cancel) },
  })
}

/**
 * The saved answer of one question dialog, one line for each question it answered,
 * in the order the dialog asked them. Null when the reply states no answers, which
 * is the reply that dismissed the dialog.
 */
export function qwenQuestionAnswerLines(request: Record<string, unknown> | undefined, result: Record<string, unknown>): string | null {
  const answers = result[QWEN_ANSWERS_FIELD]
  if (!isObject(answers))
    return null
  const questions = request ? qwenQuestions(request) : []
  const indexes = questions.length > 0 ? questions.map((_, index) => String(index)) : Object.keys(answers)
  const lines = indexes.flatMap((index) => {
    const question = questions[Number(index)]
    const label = question?.header || question?.question || `Question ${Number(index) + 1}`
    const line = labeledAnswerLine(label, Object.hasOwn(answers, index) ? [answers[index]] : [])
    return line !== null ? [line] : []
  })
  return joinAnswerLines(lines)
}
