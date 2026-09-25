import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlQuestion, QuestionOption } from '../../model/question'
import { KIRO_METHOD, KIRO_USER_INPUT_ACTION } from '~/generated/contracts/kiro-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { questionOptionValue, sendJsonRpcResult } from '../../controls/types'

/**
 * Kiro's question dialog, `_kiro/userInput`.
 *
 * Kiro asks ONE question, with a list of options or none. The reader chooses one
 * option or types an answer of their own. An option can carry sub-options, of which
 * the reader chooses any number. Kiro's own dialog starts with every sub-option
 * chosen, and the answer states the choice as `Option [Sub one, Sub two]`. Kiro
 * reads the answer as text and gives it to the model.
 *
 * The shared dialog draws one list for each question, so a sub-option list becomes a
 * question of its own after the main one. The dialog starts each list with nothing
 * selected, and Kiro's dialog starts with every sub-option chosen. So a sub-option
 * page lists the sub-options to LEAVE OUT: the empty start means every sub-option, as
 * it does in Kiro, and the reader can still leave every one out.
 *
 * The dialog shows the page of each option that has sub-options, whatever the reader
 * chose, because its pages are fixed when the request arrives. So each page states the
 * option that it belongs to, and the answer reads only the page of the chosen option.
 */

/** The words a recommended option carries beside its title, as Kiro's own dialog shows it. */
const KIRO_RECOMMENDED_SUFFIX = ' (recommended)'

/** One option as Kiro states it. */
interface KiroUserInputOption {
  title: string
  description: string
  recommended: boolean
  subOptionsLabel: string
  subOptions: KiroUserInputOption[]
}

/**
 * The options of one question, read from an untyped wire list. The model may state
 * an option as its title alone, a bare string.
 */
function kiroOptions(raw: unknown): KiroUserInputOption[] {
  if (!Array.isArray(raw))
    return []
  return raw.map(option => typeof option === 'string' ? { title: option } : option).filter(isObject).flatMap((option) => {
    const title = pickString(option, 'title')
    if (!title)
      return []
    return [{
      title,
      description: pickString(option, 'description'),
      recommended: option.recommended === true,
      subOptionsLabel: pickString(option, 'subOptionsLabel'),
      subOptions: kiroOptions(option.subOptions),
    }]
  })
}

/** One option in the shared model. The value is Kiro's own title, which the answer states. */
function questionOption(option: KiroUserInputOption): QuestionOption {
  return {
    value: option.title,
    label: option.recommended ? `${option.title}${KIRO_RECOMMENDED_SUFFIX}` : option.title,
    ...(option.description ? { description: option.description } : {}),
  }
}

/**
 * The questions of one Kiro question, in the shared model: the question itself, then
 * one question for each option that carries sub-options.
 *
 * A question with no option is a free-text question. A sub-option question lists the
 * sub-options to leave out, and it may stay empty, which states Kiro's own start.
 */
export function kiroUserInputQuestions(question: string, rawOptions: unknown): ControlQuestion[] {
  const options = kiroOptions(rawOptions)
  const main: ControlQuestion = {
    question,
    options: options.map(questionOption),
    multiSelect: false,
  }
  const subQuestions: ControlQuestion[] = options
    .filter(option => option.subOptions.length > 0)
    .map(option => ({
      header: option.title,
      question: `${option.subOptionsLabel || 'Choices'} for ${option.title}: select any to leave out`,
      options: option.subOptions.map(questionOption),
      multiSelect: true,
      allowEmpty: true,
    }))
  return [main, ...subQuestions]
}

/** Whether one control request is Kiro's question dialog. */
export function isKiroUserInputPayload(payload: Record<string, unknown>): boolean {
  return payload.method === KIRO_METHOD.UserInput
}

/** The questions of one `_kiro/userInput` request. */
export function kiroUserInputRequestQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const params = pickObject(payload, 'params')
  return kiroUserInputQuestions(pickString(params, 'question'), params?.options)
}

/**
 * The typed answer of the dialog: the text of the first page that holds one.
 *
 * The dialog saves the composer text to the page that shows when the reader types,
 * which can be a sub-option page. Kiro takes one typed answer, so any page gives it.
 */
function kiroTypedAnswer(questions: ControlQuestion[], answerState: ControlAnswerState): string {
  const texts = answerState.customTexts()
  for (let page = 0; page < questions.length; page++) {
    const typed = texts[page]?.trim() ?? ''
    if (typed)
      return typed
  }
  return ''
}

/**
 * The answer one dialog states, as Kiro's own dialog writes it, or null when the
 * reader chose nothing and typed nothing.
 *
 * A typed answer wins over a chosen option, because the reader typed it last. A
 * chosen option that carries sub-options states in brackets the ones that the reader
 * kept: every sub-option that its page did not leave out, which can be none.
 */
export function kiroUserInputAnswer(questions: ControlQuestion[], answerState: ControlAnswerState): string | null {
  const main = questions[0]
  if (!main)
    return null
  const typed = kiroTypedAnswer(questions, answerState)
  if (typed)
    return typed
  const chosen = answerState.selections()[0]?.[0]
  if (!chosen)
    return null
  const subIndex = questions.findIndex((question, index) => index > 0 && question.header === chosen)
  const sub = subIndex > 0 ? questions[subIndex] : undefined
  if (!sub)
    return chosen
  const leftOut = new Set(answerState.selections()[subIndex] ?? [])
  const kept = sub.options.map(questionOptionValue).filter(title => !leftOut.has(title))
  return `${chosen} [${kept.join(', ')}]`
}

/** The reply that answers the dialog, or dismisses it when the reader answered nothing. */
export function kiroUserInputReply(questions: ControlQuestion[], answerState: ControlAnswerState): Record<string, unknown> {
  const answer = kiroUserInputAnswer(questions, answerState)
  return answer === null
    ? { action: KIRO_USER_INPUT_ACTION.Dismissed }
    : { action: KIRO_USER_INPUT_ACTION.Answered, answer }
}

export function sendKiroUserInputResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  questions: ControlQuestion[],
  answerState: ControlAnswerState,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, kiroUserInputReply(questions, answerState))
}

/**
 * The reply that dismisses the dialog. Kiro's `dismissed` carries no reason: Kiro
 * tells the model that the reader gave no answer.
 */
export function sendKiroUserInputDismissal(onRespond: ControlResponseSender, requestId: string): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { action: KIRO_USER_INPUT_ACTION.Dismissed })
}

/** The answer one saved reply states, or null for a dismissal or an unreadable reply. */
export function kiroUserInputSavedAnswer(result: Record<string, unknown>): string | null {
  if (pickString(result, 'action') !== KIRO_USER_INPUT_ACTION.Answered)
    return null
  const answer = pickString(result, 'answer').trim()
  return answer || null
}
