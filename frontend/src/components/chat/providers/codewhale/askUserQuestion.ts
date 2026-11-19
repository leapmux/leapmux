/**
 * The Codewhale question (`request_user_input`), as LeapMux reads it in the browser.
 *
 * The runtime asks through a `user_input.required` event, `{id, request:{questions}}`,
 * and each question states `{id, header, question, options:[{label, description}],
 * allow_free_text, multi_select}`. The worker stores the request under the shared
 * `request.input` header. The Worker reads the control request itself and answers it
 * with the runtime's OWN answer list, `[{id, label, value}]`, keyed by the question id.
 *
 * This module reads three things:
 *
 *   - The questions of a stored request, for the saved-answer display.
 *   - The answers of a saved reply frame, for the same display.
 *   - The questions of a tool call, for its transcript row.
 */

import type { QuestionPrompt } from '../../model/question'
import { CODEWHALE_ANSWER_FIELD, CODEWHALE_CONTROL_PAYLOAD, CODEWHALE_ENVELOPE_FIELD, CODEWHALE_QUESTION_FIELD, CODEWHALE_USER_INPUT_FIELD } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput } from '~/utils/controlResponse'
import { questionsFromRecords } from '../questionRecords'

/**
 * The raw question records of a stored question, in the order the runtime sent them.
 *
 * `codewhaleControlResponseSummary` reads the saved answer back through this list. The
 * header input is the runtime's own `request`, and the stored event is the fallback for
 * a payload whose header lost it.
 */
export function codewhaleQuestionRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  const header = getToolInput(payload)[CODEWHALE_QUESTION_FIELD.Questions]
  const fromEvent = pickObject(pickObject(pickObject(payload, CODEWHALE_CONTROL_PAYLOAD.Event), CODEWHALE_ENVELOPE_FIELD.Payload), CODEWHALE_USER_INPUT_FIELD.Request)?.[CODEWHALE_QUESTION_FIELD.Questions]
  const questions = Array.isArray(header)
    ? header
    : fromEvent
  return Array.isArray(questions)
    ? questions.filter(isObject)
    : []
}

/**
 * The text one question shows: the sentence, or the header when it states only that.
 * Empty for a record nothing can answer.
 */
export function codewhaleQuestionText(question: Record<string, unknown>): string {
  return pickString(question, CODEWHALE_QUESTION_FIELD.Question) || pickString(question, CODEWHALE_QUESTION_FIELD.Header)
}

/** The questions of a `request_user_input` TOOL CALL, for the transcript row that draws it. */
export function codewhaleQuestionsFromToolInput(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input[CODEWHALE_QUESTION_FIELD.Questions],
    (question) => {
      const header = pickString(question, CODEWHALE_QUESTION_FIELD.Header)
      const text = codewhaleQuestionText(question)
      return {
        ...(header && header !== text
          ? { header }
          : {}),
        question: text,
      }
    },
    (option) => {
      const label = pickString(option, CODEWHALE_QUESTION_FIELD.Label)
      if (!label)
        return null
      const description = pickString(option, CODEWHALE_QUESTION_FIELD.Description)
      return {
        label,
        ...(description
          ? { description }
          : {}),
      }
    },
  )
}

/**
 * The saved answers of one question, read back out of a reply frame's answer list.
 *
 * Several entries share an id for a multiple choice, and each contributes its value.
 */
export function codewhaleAnswerValues(answers: unknown, id: string): string[] {
  if (!Array.isArray(answers))
    return []
  return answers
    .filter(isObject)
    .filter(answer => pickString(answer, CODEWHALE_ANSWER_FIELD.ID) === id)
    .map(answer => pickString(answer, CODEWHALE_ANSWER_FIELD.Value).trim())
    .filter(Boolean)
}
