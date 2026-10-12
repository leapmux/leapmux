/**
 * Factory Droid's question requests: recognize one, read its questions, and
 * answer it.
 *
 * Droid asks through `droid.ask_user`, a server->client request whose params
 * carry `toolCallId` and `questions`. The worker publishes a control request
 * that keeps the `index`, the words, the options and the kind of each question,
 * in Droid's own order (`onAskUser`). That control request is the payload that
 * this module reads.
 *
 * Droid identifies each answer by the `index` of its question, and takes each
 * answer as one string. So the decision that this module builds answers each
 * question by its `index`, in the neutral approve envelope. The worker checks the
 * answers against the stored request and writes Droid's own reply,
 * `{ cancelled, answers }`, where each answer also states the words of its question.
 */
import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { DROID_ASK_USER_FIELD, DROID_REPLY, DROID_REQUEST_TYPE } from '~/generated/contracts/droid-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'

/** One question of a stored question request, as Droid states it. */
interface DroidQuestion {
  /** Droid's own number for the question. Undefined when the request states no whole number. */
  index: number | undefined
  question: string
  options: string[]
  multiSelect: boolean
}

/** The text that Droid's own TUI puts between the parts of one answer. */
const ANSWER_PART_SEPARATOR = ', '

/** Whether a stored control payload is a question request. */
export function droidIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === DROID_REQUEST_TYPE.AskUser
}

/**
 * The questions of a stored question request, in the order that the request
 * declares.
 *
 * The shared control and the answer both read this one list, so the position of a
 * question in the control is its position here.
 */
function droidQuestions(payload: Record<string, unknown>): DroidQuestion[] {
  const raw: unknown = payload[DROID_ASK_USER_FIELD.Questions]
  const questions: unknown[] = Array.isArray(raw) ? raw : []
  return questions.filter(isObject).map((question) => {
    const index: unknown = question[DROID_ASK_USER_FIELD.Index]
    const rawOptions: unknown = question[DROID_ASK_USER_FIELD.Options]
    const options: unknown[] = Array.isArray(rawOptions) ? rawOptions : []
    return {
      index: typeof index === 'number' && Number.isInteger(index) ? index : undefined,
      question: pickString(question, DROID_ASK_USER_FIELD.Question),
      options: options.filter((option): option is string => typeof option === 'string'),
      multiSelect: question[DROID_ASK_USER_FIELD.MultiSelect] === true,
    }
  })
}

/** The questions of a stored question request, for the shared control. */
export function droidQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  return droidQuestions(payload).map(question => ({
    question: question.question,
    options: question.options.map(label => ({ value: label, label })),
    multiSelect: question.multiSelect,
  }))
}

/**
 * The answer to one question, as Droid's own TUI writes it.
 *
 * A multiple-choice question takes every offered pick, in the order of the
 * options, and then the typed text, joined with ", ". A single-choice question
 * takes its pick, or the typed text when nothing is picked. The control keeps
 * the pick and the typed text exclusive there, as Droid's TUI does, so a pick
 * wins when both still arrive. A question with neither takes "", which Droid's
 * TUI also sends.
 */
function droidAnswerText(question: DroidQuestion, picked: readonly string[], typed: string): string {
  const chosen = question.options.filter(option => picked.includes(option))
  const text = typed.trim()
  if (question.multiSelect)
    return [...chosen, ...(text ? [text] : [])].join(ANSWER_PART_SEPARATOR)
  return chosen[0] ?? text
}

/**
 * The control response that answers every question of one request.
 *
 * Each answer states the `index` of its question. When the request states no
 * whole number for a question, its answer goes without an index. The worker then
 * refuses the whole set, so the reader sees the refusal, rather than Droid
 * getting an answer that it cannot pair with a question.
 */
export function buildDroidAnswer(requestId: string, payload: Record<string, unknown>, state: ControlAnswerState): Record<string, unknown> {
  const answers = droidQuestions(payload).map((question, position) => ({
    ...(question.index === undefined ? {} : { [DROID_ASK_USER_FIELD.Index]: question.index }),
    [DROID_ASK_USER_FIELD.Answer]: droidAnswerText(question, state.selections()[position] ?? [], state.customTexts()[position] ?? ''),
  }))
  return buildControlResponseEnvelope(requestId, { behavior: 'allow', [DROID_REPLY.Answers]: answers })
}
