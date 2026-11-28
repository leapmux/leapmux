import type { QuestionPrompt } from '../../model/question'
import { isObject, pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

const text = (value: string) => value.replaceAll('\r\n', '\n')

/**
 * The question records one of Pi's four question tools sends.
 *
 * Each of them states a LIST under `questions`. A tool that asks exactly one states
 * the record at the root instead, so the root is read as a list of one -- which
 * yields nothing when it carries no `question` field either.
 */
function piQuestionRecords(args: Record<string, unknown>): Record<string, unknown>[] {
  const questions = args.questions
  return Array.isArray(questions)
    ? questions.filter(isObject)
    : [args]
}

/**
 * The questions a Pi call asked, in the shape the shared body builder reads.
 *
 * Pi's four question tools -- `ask_user_question`, `plan_mode_question`,
 * `goal_question` and `goal_questionnaire` -- spell one question with the same field
 * names. The Worker reads those fields too, when it matches a question dialog to its
 * call (`control_source.go` in the Go `pi` package). An option carries a sentence and
 * a worked example beside its label, and the row draws BOTH: the question control
 * draws them, so a reader who comes back to the row has to be able to tell what the
 * alternatives actually were.
 */
export function piQuestionsFromArgs(args: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    piQuestionRecords(args),
    (record) => {
      const header = text(pickString(record, 'header'))
      return {
        ...(header
          ? { header }
          : {}),
        question: text(pickString(record, 'question')),
      }
    },
    (option) => {
      const label = text(pickString(option, 'label'))
      if (!label)
        return null
      const description = text(pickString(option, 'description'))
      const preview = pickString(option, 'preview', undefined)
      return {
        label,
        ...(description
          ? { description }
          : {}),
        ...(preview !== undefined
          ? { preview }
          : {}),
      }
    },
  )
}

/**
 * The header words a question row states, or none when the row composes its own.
 *
 * One question IS the header, because the row has room for it and nothing else states
 * it. Several cannot share one line, so the shared renderer states their count, and
 * this gives it nothing to override.
 */
export function piQuestionTitle(questions: QuestionPrompt[]): string | undefined {
  // One question is the list; the `length === 1` test pins the indexed read.
  return questions.length === 1
    ? questions[0]?.question
    : undefined
}
