/**
 * Kimi Code's question requests: recognize one, read its questions, and answer it.
 *
 * The server announces a question as `event.question.requested`, and the worker
 * publishes that payload verbatim as the control request. Each question and each
 * option carries an id of the server's own (`q_0`, `opt_0_1`), and an answer names
 * those ids rather than the words on screen. So an option's VALUE here is its id,
 * which is what the shared control keeps as the selection, and the answer this module
 * builds is the server's own answer shape.
 */

import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion, QuestionPrompt } from '../../model/question'
import { KIMI_ANSWER_KIND, KIMI_EVENT, KIMI_REPLY } from '~/generated/contracts/kimi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'
import { questionsFromRecords } from '../questionRecords'

/** The raw question records of a stored question request, in the order it declares. */
export function kimiQuestionRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  return Array.isArray(payload.questions) ? payload.questions.filter(isObject) : []
}

/** Whether a stored control payload is a question request. */
export function kimiIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === KIMI_EVENT.QuestionRequested
}

/**
 * The questions of a stored question request, for the shared control.
 *
 * Every question may be skipped: the server takes a `skipped` answer for any of them,
 * so none needs a selection before the reader can submit.
 */
export function kimiQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  return kimiQuestionRecords(payload).flatMap((record) => {
    const id = pickString(record, 'id')
    const question = pickString(record, 'question') || pickString(record, 'header')
    if (!id || !question)
      return []
    const header = pickString(record, 'header')
    const options = Array.isArray(record.options) ? record.options.filter(isObject) : []
    const built: ControlQuestion = {
      id,
      question,
      options: options.flatMap((option) => {
        const value = pickString(option, 'id')
        const label = pickString(option, 'label')
        if (!value || !label)
          return []
        const description = pickString(option, 'description')
        return [{ value, label, ...(description ? { description } : {}) }]
      }),
      allowEmpty: true,
    }
    if (header && header !== question)
      built.header = header
    if (record.multi_select === true)
      built.multiSelect = true
    return [built]
  })
}

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
      return { ...(header ? { header } : {}), question: pickString(question, 'question') }
    },
    (option) => {
      const label = pickString(option, 'label')
      if (!label)
        return null
      const description = pickString(option, 'description')
      return { label, ...(description ? { description } : {}) }
    },
  )
}

/** One answer in the server's own shape. */
export type KimiAnswer
  = | { kind: typeof KIMI_ANSWER_KIND.Single, option_id: string }
    | { kind: typeof KIMI_ANSWER_KIND.Multi, option_ids: string[] }
    | { kind: typeof KIMI_ANSWER_KIND.Other, text: string }
    | { kind: typeof KIMI_ANSWER_KIND.MultiWithOther, option_ids: string[], other_text: string }
    | { kind: typeof KIMI_ANSWER_KIND.Skipped }

/**
 * The answer to ONE question, from what the reader selected and typed.
 *
 * A single-choice question takes either a choice or the reader's own text, never both,
 * because the control keeps the two exclusive. A multiple-choice question takes both
 * together, which the server spells `multi_with_other`.
 */
export function kimiAnswer(question: ControlQuestion, selected: readonly string[], typed: string): KimiAnswer {
  const offered = new Set(question.options.map(option => option.value ?? option.label))
  const picked = selected.filter(value => offered.has(value))
  const text = typed.trim()
  if (question.multiSelect) {
    if (picked.length > 0 && text)
      return { kind: KIMI_ANSWER_KIND.MultiWithOther, option_ids: picked, other_text: text }
    if (picked.length > 0)
      return { kind: KIMI_ANSWER_KIND.Multi, option_ids: picked }
  }
  else if (picked[0] !== undefined) {
    return { kind: KIMI_ANSWER_KIND.Single, option_id: picked[0] }
  }
  if (text)
    return { kind: KIMI_ANSWER_KIND.Other, text }
  return { kind: KIMI_ANSWER_KIND.Skipped }
}

/**
 * The control response that answers every question of one request.
 *
 * It travels in the neutral approve envelope, with the answers beside the behavior. The
 * worker checks them against the stored request and posts the server's own body.
 */
export function buildKimiAnswers(requestId: string, questions: ControlQuestion[], state: ControlAnswerState): Record<string, unknown> {
  const answers: Record<string, KimiAnswer> = {}
  questions.forEach((question, index) => {
    if (!question.id)
      return
    answers[question.id] = kimiAnswer(question, state.selections()[index] ?? [], state.customTexts()[index] ?? '')
  })
  return buildControlResponseEnvelope(requestId, { behavior: 'allow', [KIMI_REPLY.Answers]: answers })
}
