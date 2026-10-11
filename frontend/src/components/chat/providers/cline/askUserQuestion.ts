/**
 * Cline's question requests: recognize one, read its question, and answer it.
 *
 * Cline asks a question through its `ask_question` tool, whose executor the worker
 * owns: the hub sends the worker a `capability.requested` for the question, and the
 * worker publishes that event verbatim as the control request.
 *
 *   {"event":"capability.requested","payload":{"requestId":"capreq_1",
 *    "capabilityName":"tool_executor.askQuestion",
 *    "payload":{"executor":"askQuestion","args":["Which color?",["Red","Blue"]],
 *               "context":{"toolCallId":"call_1", ...}}}}
 *
 * The answer is one line of text: the option the reader picked, or the reader's own
 * words. It travels in the neutral approve envelope, and the worker turns it into
 * Cline's own capability reply.
 */

import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { CLINE_CAPABILITY, CLINE_EVENT, CLINE_QUESTION_ANSWER } from '~/generated/contracts/cline-protocol'
import { pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'
import { clinePayload } from './protocol'

/** The capability request of a stored control payload, when it is a question. */
function questionRequest(payload: Record<string, unknown>): Record<string, unknown> | null {
  const request = clinePayload(payload, CLINE_EVENT.CapabilityRequested)
  return request && pickString(request, 'capabilityName') === CLINE_CAPABILITY.AskQuestion ? request : null
}

/** Whether a stored control payload is a question request. */
export function clineIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return questionRequest(payload) !== null
}

/**
 * The question of a stored question request, for the shared control. The executor's
 * arguments are the question and its options, in that order.
 */
export function clineQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  const request = questionRequest(payload)
  const args = pickObject(request, 'payload')?.args
  if (!Array.isArray(args))
    return []
  const [question, options] = args
  if (typeof question !== 'string' || question.trim() === '')
    return []
  return [{
    question: question.trim(),
    options: stringArray(options).map(label => ({ value: label, label })),
  }]
}

/**
 * The control response that answers one question: the reader's own words when they
 * typed any, and the option they picked otherwise.
 */
export function buildClineAnswer(requestId: string, questions: ControlQuestion[], state: ControlAnswerState): Record<string, unknown> {
  const typed = (state.customTexts()[0] ?? '').trim()
  const offered = new Set((questions[0]?.options ?? []).map(option => option.value ?? option.label))
  const picked = (state.selections()[0] ?? []).find(value => offered.has(value)) ?? ''
  return buildControlResponseEnvelope(requestId, { behavior: 'allow', [CLINE_QUESTION_ANSWER.Answer]: typed || picked })
}
