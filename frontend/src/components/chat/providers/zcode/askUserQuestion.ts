/**
 * The questions of ZCode's `AskUserQuestion` tool call, as its transcript row draws
 * them.
 *
 * The Worker reads the control request that asks the same questions
 * (`zcodeQuestions` in the Go `zcode` package), and both the banner and the
 * saved-answer display read the prompt that it records.
 */

import type { QuestionPrompt } from '../../model/question'
import { pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

/**
 * The text that one question shows: the question itself, or the header when the
 * app-server sent a header alone. An empty result marks a record that asks nothing.
 *
 * The Worker reads the same text for the prompt of the control request, and keys the
 * saved answer by it.
 */
export function zcodeQuestionText(question: Record<string, unknown>): string {
  return pickString(question, 'question') || pickString(question, 'header')
}

/**
 * The questions of an `AskUserQuestion` TOOL CALL, for the row that draws it.
 *
 * The stored control REQUEST carries the same questions under `params`. This reads the
 * tool call's own input instead, because a transcript row is built from the tool frame
 * and the control request is a separate message that a replay may not have beside it.
 *
 * ZCode's option sets `value` to the label, so either field standing in for the
 * other keeps the option readable.
 */
export function zcodeQuestionsFromToolInput(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return {
        ...(header
          ? { header }
          : {}),
        question: zcodeQuestionText(question),
      }
    },
    (option) => {
      const label = pickString(option, 'label') || pickString(option, 'value')
      if (!label)
        return null
      const description = pickString(option, 'description')
      const preview = pickString(option, 'preview')
      return {
        label,
        ...(description
          ? { description }
          : {}),
        ...(preview
          ? { preview }
          : {}),
      }
    },
  )
}
