import type { ControlQuestion, QuestionOption } from '../../../model/question'
import type { QuestionAnswer } from '../../../model/tools/question'
import { OH_MY_PI_ASK_OPTION, OH_MY_PI_ASK_QUESTION } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { ohMyPiChoiceMarkup } from './choiceMarkup'

/** Read choice fields from the bridge; retained HTML supplies their browser presentation. */
export function ohMyPiQuestionPrompts(records: unknown): Array<ControlQuestion & { id: string }> {
  if (!Array.isArray(records))
    return []
  return records.filter(isObject).flatMap((record) => {
    const question = pickString(record, OH_MY_PI_ASK_QUESTION.Question)
    if (!question)
      return []
    const options: QuestionOption[] = Array.isArray(record[OH_MY_PI_ASK_QUESTION.Options])
      ? (record[OH_MY_PI_ASK_QUESTION.Options] as unknown[]).filter(isObject).flatMap((option) => {
          const label = pickString(option, OH_MY_PI_ASK_OPTION.Label)
          if (!label)
            return []
          const presentation = ohMyPiChoiceMarkup(pickString(option, OH_MY_PI_ASK_OPTION.Markup), label)
          const value = pickString(option, OH_MY_PI_ASK_OPTION.Value, undefined)
          const { description, preview } = presentation
          return [
            {
              label: presentation.label,
              ...(value !== undefined
                ? { value }
                : {}),
              ...(description
                ? { description }
                : {}),
              ...(preview?.trim()
                ? { preview }
                : {}),
            },
          ]
        })
      : []
    const header = pickString(record, OH_MY_PI_ASK_QUESTION.Header)
    const multiSelect = record[OH_MY_PI_ASK_QUESTION.Multi] === true
    const allowEmpty = record[OH_MY_PI_ASK_QUESTION.AllowEmpty]
    const allowCustomText = record[OH_MY_PI_ASK_QUESTION.AllowCustomText]
    const offered = new Set(options.map(option => option.value ?? option.label))
    const initial = [
      ...new Set(stringArray(record[OH_MY_PI_ASK_QUESTION.InitialSelections]).filter(value => offered.has(value))),
    ]
    if (!multiSelect)
      initial.splice(1)
    return [
      {
        id: pickString(record, OH_MY_PI_ASK_QUESTION.ID),
        question,
        ...(header
          ? { header }
          : {}),
        options,
        ...(multiSelect
          ? { multiSelect: true }
          : {}),
        ...(typeof allowEmpty === 'boolean'
          ? { allowEmpty }
          : {}),
        ...(typeof allowCustomText === 'boolean'
          ? { allowCustomText }
          : {}),
        ...(Array.isArray(record[OH_MY_PI_ASK_QUESTION.InitialSelections])
          ? { initialSelections: initial }
          : {}),
      },
    ]
  })
}

/** Reads the native form result. Empty strings are valid answers to optional fields. */
export function ohMyPiQuestionAnswers(details: Record<string, unknown>): QuestionAnswer[] | null {
  if (!Array.isArray(details.answers))
    return null
  const answers = details.answers.filter(isObject).flatMap((record) => {
    const answer = pickString(record, 'answer', undefined)
    return answer !== undefined
      ? [
          { header: pickString(record, 'question') || pickString(record, 'name') || 'Answer', answer },
        ]
      : []
  })
  return answers.length > 0
    ? answers
    : null
}
