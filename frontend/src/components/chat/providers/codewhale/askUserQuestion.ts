/**
 * The Codewhale question: `request_user_input` -> the shared question control.
 *
 * The runtime asks through a `user_input.required` event, `{id, request:{questions}}`,
 * and each question states `{id, header, question, options:[{label, description}],
 * allow_free_text, multi_select}`. The worker stores the request under the shared
 * `request.input` header, so the shared control reads the questions through this one
 * reader, and the saved-answer display reads the same list back.
 *
 * The runtime takes its OWN answer list, `[{id, label, value}]`, keyed by the question
 * id: one entry for each chosen option, and a free-text answer labelled `Other`. The
 * control builds that list here, so the worker forwards it without reading question
 * text back into ids.
 */

import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion, QuestionPrompt } from '../../model/question'
import { CODEWHALE_ANSWER_FIELD, CODEWHALE_ANSWER_LABEL, CODEWHALE_CONTROL_PAYLOAD, CODEWHALE_ENVELOPE_FIELD, CODEWHALE_EVENT, CODEWHALE_QUESTION_FIELD, CODEWHALE_TOOL, CODEWHALE_USER_INPUT_FIELD } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { questionsFromRecords } from '../questionRecords'

/** One answer in the runtime's own shape. */
export interface CodewhaleAnswer {
  id: string
  label: string
  value: string
}

/**
 * Whether a stored control payload is a Codewhale question.
 *
 * The runtime's own event decides, and the tool name the worker put in the header
 * confirms it: an approval of any tool carries `approval.required` instead.
 */
export function codewhaleIsQuestionRequest(payload: Record<string, unknown>): boolean {
  const event = pickObject(payload, CODEWHALE_CONTROL_PAYLOAD.Event)
  return pickString(event, CODEWHALE_ENVELOPE_FIELD.Event) === CODEWHALE_EVENT.UserInputRequired
    && getToolName(payload) === CODEWHALE_TOOL.RequestUserInput
}

/**
 * The raw question records of a stored question, in the order the runtime sent them.
 *
 * This is the ONE question list of the provider: the control answers it, and
 * `codewhaleControlResponseSummary` reads the saved answer back through it. The header
 * input is the runtime's own `request`, and the stored event is the fallback for a
 * payload whose header lost it.
 */
export function codewhaleQuestionRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  const header = getToolInput(payload)[CODEWHALE_QUESTION_FIELD.Questions]
  const fromEvent = pickObject(pickObject(pickObject(payload, CODEWHALE_CONTROL_PAYLOAD.Event), CODEWHALE_ENVELOPE_FIELD.Payload), CODEWHALE_USER_INPUT_FIELD.Request)?.[CODEWHALE_QUESTION_FIELD.Questions]
  const questions = Array.isArray(header) ? header : fromEvent
  return Array.isArray(questions) ? questions.filter(isObject) : []
}

/**
 * The text one question shows: the sentence, or the header when it states only that.
 * Empty for a record nothing can answer.
 */
export function codewhaleQuestionText(question: Record<string, unknown>): string {
  return pickString(question, CODEWHALE_QUESTION_FIELD.Question) || pickString(question, CODEWHALE_QUESTION_FIELD.Header)
}

/** The options of one question. An option with no label has nothing to click, so it is dropped. */
function codewhaleOptions(question: Record<string, unknown>): ControlQuestion['options'] {
  const options = question[CODEWHALE_QUESTION_FIELD.Options]
  return (Array.isArray(options) ? options.filter(isObject) : []).flatMap((option) => {
    const label = pickString(option, CODEWHALE_QUESTION_FIELD.Label)
    if (!label)
      return []
    const description = pickString(option, CODEWHALE_QUESTION_FIELD.Description)
    return [{ label, ...(description ? { description } : {}) }]
  })
}

/**
 * The questions of a stored Codewhale question, for the shared control.
 *
 * A question needs its id: the runtime matches an answer by it, so a record with no id
 * could be answered in a way the runtime never reads.
 */
export function codewhaleQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  return codewhaleQuestionRecords(payload).flatMap((raw) => {
    const id = pickString(raw, CODEWHALE_QUESTION_FIELD.ID)
    const text = codewhaleQuestionText(raw)
    if (!id || !text)
      return []
    const header = pickString(raw, CODEWHALE_QUESTION_FIELD.Header)
    return [{
      id,
      question: text,
      options: codewhaleOptions(raw),
      ...(header && header !== text ? { header } : {}),
      ...(raw[CODEWHALE_QUESTION_FIELD.MultiSelect] === true ? { multiSelect: true } : {}),
    }]
  })
}

/** The questions of a `request_user_input` TOOL CALL, for the transcript row that draws it. */
export function codewhaleQuestionsFromToolInput(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input[CODEWHALE_QUESTION_FIELD.Questions],
    (question) => {
      const header = pickString(question, CODEWHALE_QUESTION_FIELD.Header)
      const text = codewhaleQuestionText(question)
      return { ...(header && header !== text ? { header } : {}), question: text }
    },
    (option) => {
      const label = pickString(option, CODEWHALE_QUESTION_FIELD.Label)
      if (!label)
        return null
      const description = pickString(option, CODEWHALE_QUESTION_FIELD.Description)
      return { label, ...(description ? { description } : {}) }
    },
  )
}

/**
 * The runtime's answer list for the reader's choices.
 *
 * A chosen option answers with its own label as both label and value. A multiple
 * choice sends one entry for each option it holds, all with the same id -- the list
 * the runtime reads, which a joined string could not state. Free text answers with the
 * `Other` label. The two are alternatives, as the shared control keeps them: a choice
 * wins over the text beside it.
 */
export function codewhaleAnswers(questions: ControlQuestion[], state: ControlAnswerState): CodewhaleAnswer[] {
  const selections = state.selections()
  const customTexts = state.customTexts()
  return questions.flatMap((question, index) => {
    const id = question.id
    if (!id)
      return []
    const chosen = selections[index] ?? []
    if (chosen.length > 0)
      return chosen.map(label => codewhaleAnswer(id, label, label))
    const text = customTexts[index]?.trim()
    return text ? [codewhaleAnswer(id, CODEWHALE_ANSWER_LABEL.Other, text)] : []
  })
}

/** One answer, keyed by the contract's field names, which the worker forwards as they stand. */
function codewhaleAnswer(id: string, label: string, value: string): CodewhaleAnswer {
  return { [CODEWHALE_ANSWER_FIELD.ID]: id, [CODEWHALE_ANSWER_FIELD.Label]: label, [CODEWHALE_ANSWER_FIELD.Value]: value }
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
