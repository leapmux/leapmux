/**
 * The Codewhale question (`request_user_input`), as LeapMux reads it in the browser.
 *
 * The runtime asks through a `user_input.required` event, `{id, request:{questions}}`,
 * and each question states `{id, header, question, options:[{label, description}],
 * allow_free_text, multi_select}`. The worker stores the request under the shared
 * `request.input` header. The Worker reads the control request itself and answers it
 * with the runtime's OWN answer list, `[{id, label, value}]`, keyed by the question id.
 *
 * This module reads the questions of a tool call, for its transcript row.
 */

import type { QuestionPrompt } from '../../model/question'
import { CODEWHALE_QUESTION_FIELD } from '~/generated/contracts/codewhale-protocol'
import { pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

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
