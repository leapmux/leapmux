/**
 * Letta Code's question requests: recognize one, read its questions, and answer
 * it.
 *
 * Letta asks through `AskUserQuestion`, whose `can_use_tool` control request
 * carries the tool call and its `tool_input`. The worker publishes that request
 * verbatim as the control request, so the payload the reader sees is the one
 * this module reads.
 *
 * The answer is Letta's own `approval_response` decision: the flat
 * `{kind, request_id, decision}` payload whose `updated_input` holds the
 * original questions and an `answers` map keyed by question text.
 */
import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { LETTA_DELTA_FIELD } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'

/** Whether a stored control payload is a question request. */
export function lettaIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === 'ask_user'
}

/** The questions of a stored question request, for the shared control. */
export function lettaQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  const toolInput = pickObject(payload, LETTA_DELTA_FIELD.ToolInput) ?? payload
  const questions = Array.isArray(toolInput.questions) ? toolInput.questions : []
  return questions.filter(isObject).map((question) => {
    const options = Array.isArray(question.options) ? question.options : []
    return {
      question: pickString(question, 'question'),
      // Letta's options are `{label, description}` OBJECTS, not bare strings.
      // Reading them as strings dropped every one and the banner drew a
      // question with no choices.
      options: options.flatMap((option) => {
        const label = typeof option === 'string' ? option : pickString(option, 'label')
        return label ? [{ value: label, label }] : []
      }),
      multiSelect: question.multiSelect === true,
    }
  })
}

/**
 * The control response that answers one question: the option the reader picked
 * for each question, keyed by the question text.
 */
export function buildLettaAnswer(requestId: string, questions: ControlQuestion[], state: ControlAnswerState): Record<string, unknown> {
  const answers: Record<string, string> = {}
  questions.forEach((question, index) => {
    const picked = (state.selections()[index] ?? []).find(value => question.options.some(option => option.value === value))
    answers[question.question] = picked ?? state.customTexts()[index] ?? ''
  })
  return buildControlResponseEnvelope(requestId, { behavior: 'allow', answers })
}
