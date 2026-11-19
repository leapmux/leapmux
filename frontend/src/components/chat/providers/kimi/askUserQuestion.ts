/**
 * Kimi Code's questions, as LeapMux reads them in the browser.
 *
 * The server announces a question through an `interaction` frame. The worker stores
 * the frame unchanged, and its nested request holds the questions. Each question and
 * each option carries an id of the server's own (`q_0`, `opt_0_1`), and the saved
 * answer states those ids rather than the words on screen. The Worker reads the
 * control request itself, answers it in the server's own answer shape, and records
 * the neutral decision that words the saved answer.
 *
 * This module reads the questions of an `AskUserQuestion` tool call, which its
 * transcript row draws.
 */

import type { QuestionPrompt } from '../../model/question'
import { pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

/**
 * The questions of an `AskUserQuestion` TOOL CALL, for the row that draws it.
 *
 * The call's own arguments state the questions without ids -- the server assigns
 * those when it asks -- so this reads the words alone.
 */
export function kimiQuestionsFromToolInput(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return {
        ...(header
          ? { header }
          : {}),
        question: pickString(question, 'question'),
      }
    },
    (option) => {
      const label = pickString(option, 'label')
      if (!label)
        return null
      const description = pickString(option, 'description')
      return {
        label,
        ...(description
          ? { description }
          : {}),
      }
    },
  )
}
