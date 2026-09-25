import type { QuestionPrompt } from '../../../model/question'
import type { QuestionRequest, QuestionResult } from '../../../model/tools/question'
import { pickString, stringArray } from '~/lib/jsonPick'
import { outputText } from './toolCommon'

/**
 * Cline's `ask_question` tool.
 *
 *   {question, options: ["A", "B"]}  ->  "the answer"
 *
 * One call asks one question with two to five options, and the reader answers with
 * one of them or with words of their own. The answer is the call's result.
 */

/** The question one call states. */
export function clineQuestionPrompt(args: Record<string, unknown>): QuestionPrompt {
  return {
    question: pickString(args, 'question'),
    options: stringArray(args.options).map(label => ({ label })),
  }
}

/** The request one call states. */
export function clineQuestionRequest(args: Record<string, unknown>): QuestionRequest {
  const prompt = clineQuestionPrompt(args)
  return { questions: prompt.question ? [prompt] : [] }
}

/** The answer one finished call returned. */
export function clineQuestionResult(request: QuestionRequest, output: unknown): QuestionResult {
  const answer = outputText(output).trim()
  const header = request.questions[0]?.question ?? ''
  return { answers: [{ header, answer: answer || null }] }
}
