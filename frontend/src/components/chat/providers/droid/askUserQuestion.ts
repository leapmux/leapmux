/**
 * Factory Droid's question requests: recognize one, read its questions, and
 * answer it.
 *
 * Droid asks through `droid.ask_user`, a server->client request whose params
 * carry `toolCallId` and `questions`. The worker publishes that request verbatim
 * as the control request, so the payload the reader sees is the one this module
 * reads.
 *
 * The answer is Droid's own body: `{ cancelled, answers }`, where `answers` maps
 * each question text to the option the reader picked. It travels in the neutral
 * approve envelope, and the worker turns it into Droid's own reply.
 */
import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { DROID_ASK_USER_FIELD } from '~/generated/contracts/droid-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'

/** Whether a stored control payload is a question request. */
export function droidIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === 'ask_user_request'
}

/** The questions of a stored question request, for the shared control. */
export function droidQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  const raw: unknown = payload[DROID_ASK_USER_FIELD.Questions]
  const questions: unknown[] = Array.isArray(raw) ? raw : []
  return questions.filter(isObject).map((question) => {
    const rawOptions: unknown = question[DROID_ASK_USER_FIELD.Options]
    const options: unknown[] = Array.isArray(rawOptions) ? rawOptions : []
    return {
      question: pickString(question, DROID_ASK_USER_FIELD.Question),
      options: options
        .filter((option): option is string => typeof option === 'string')
        .map((label: string) => ({ value: label, label })),
      multiSelect: question[DROID_ASK_USER_FIELD.MultiSelect] === true,
    }
  })
}

/**
 * The control response that answers one question: the option the reader picked
 * for each question, keyed by the question text.
 */
export function buildDroidAnswer(requestId: string, questions: ControlQuestion[], state: ControlAnswerState): Record<string, unknown> {
  const answers: Record<string, string> = {}
  questions.forEach((question, index) => {
    const picked = (state.selections()[index] ?? []).find(value => question.options.some(option => option.value === value))
    if (picked !== undefined)
      answers[question.question] = picked
    else
      answers[question.question] = state.customTexts()[index] ?? ''
  })
  return buildControlResponseEnvelope(requestId, { behavior: 'allow', answers })
}
