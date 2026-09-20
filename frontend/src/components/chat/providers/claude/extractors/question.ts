import type { QuestionPrompt } from '../../../model/question'
import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { QuestionRequest } from '../../../model/tools/question'
import type { ClaudeToolRow } from './toolCommon'
import { isObject, pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { unparsedResult } from '../../../model/toolCall'
import { claudeToolFailureResult } from './failure'

/** The questions of an `AskUserQuestion` call, as the tool spells them. */
function questionList(source: Record<string, unknown> | null | undefined): Record<string, unknown>[] {
  const questions = source?.questions
  return Array.isArray(questions) ? questions.filter(isObject) : []
}

/** The header of one question, which the tool caps at twelve characters. */
function questionHeader(question: Record<string, unknown>): string {
  return pickString(question, 'header') || pickString(question, 'question')
}

/**
 * The header of an `AskUserQuestion` row.
 *
 * One question IS the header, because the row has room for it and nothing else
 * states it. Several cannot share one line, so the row states how many it asked
 * and the body lists them.
 */
export function claudeAskUserQuestionTitle(input: Record<string, unknown>): string {
  const questions = questionList(input)
  if (questions.length === 0)
    return 'Question'
  return questions.length === 1
    // The length check keeps the index in range; `?? {}` is the type-level guard alone.
    ? pickString(questions[0], 'question') || questionHeader(questions[0] ?? {}) || 'Question'
    : pluralize(questions.length, 'question')
}

/**
 * The questions a call asked, in the shape the shared body builder reads.
 *
 * An option carries a sentence and a worked example beside its label, and the row
 * draws BOTH -- the control banner above it does, so a reader who comes back to the
 * row has to be able to tell what the alternatives actually were.
 */
export function claudeQuestions(input: Record<string, unknown>): QuestionPrompt[] {
  return questionList(input).flatMap((question) => {
    const text = pickString(question, 'question') || questionHeader(question)
    if (!text)
      return []
    const header = pickString(question, 'header')
    const options = Array.isArray(question.options) ? question.options.filter(isObject) : []
    return [{
      // Each optional half rides only when the tool stated it; a blank one is
      // no header, not an empty one.
      ...(header ? { header } : {}),
      question: text,
      options: options.flatMap((option) => {
        const label = pickString(option, 'label')
        if (!label)
          return []
        const description = pickString(option, 'description')
        const preview = pickString(option, 'preview')
        return [{
          label,
          ...(description ? { description } : {}),
          ...(preview ? { preview } : {}),
        }]
      }),
    }]
  })
}

/**
 * The question pair: what the call asked, and the answers the reader chose.
 *
 * Claude keys its answers by the full question text; an older LeapMux build
 * keyed them by the header. Both are read, so a saved row keeps its answer.
 */
export function claudeQuestionSpec(request: QuestionRequest, args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'question'> {
  // The row's header word: one question states itself, several state their count.
  const title = claudeAskUserQuestionTitle(args.input)
  if (!result || result.role !== 'result')
    return { kind: 'question', request, title }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'question', request, title, result: failure }
  const questions = questionList(result.toolUseResult)
  // No structured payload means this build could not read the answer, which is a
  // different statement from "the reader answered nothing": an empty `answers`
  // list renders as an empty body and loses the text the result did carry.
  if (questions.length === 0)
    return { kind: 'question', request, title, result: unparsedResult(result.resultContent) }
  const answers = isObject(result.toolUseResult?.answers) ? result.toolUseResult.answers : {}
  const chosen = questions.flatMap((question) => {
    const header = questionHeader(question)
    const text = pickString(question, 'question')
    const answer = pickString(answers, text) || pickString(answers, header)
    return [{ header: header || text, answer: answer || null }]
  })
  return { kind: 'question', request, title, result: { answers: chosen } }
}
