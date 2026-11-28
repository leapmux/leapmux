import type { ControlQuestion, QuestionOption } from '../../model/question'
import type { QuestionAnswer } from '../../model/tools/question'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'

interface AntigravityQuestionRecord {
  index: number
  id: string
  prompt: ControlQuestion | null
}

export function antigravityQuestionRecords(source: unknown): AntigravityQuestionRecord[] {
  if (!Array.isArray(source))
    return []
  return source.map((record, index) => {
    if (!isObject(record))
      return { index, id: '', prompt: null }
    const id = pickString(record, 'id')
    const question = pickString(record, 'question')
    const options = Array.isArray(record.options)
      ? record.options.filter(isObject).map(questionOption).filter(option => option.label.trim())
      : []
    const prompt = question.trim()
      ? {
          question,
          options,
          multiSelect: record.isMultiSelect === true,
          ...(id
            ? { id }
            : {}),
        }
      : null
    return { index, id, prompt }
  })
}

function questionOption(option: Record<string, unknown>): QuestionOption {
  const label = pickString(option, 'text')
  const value = pickString(option, 'id', undefined)
  return value === undefined
    ? { label }
    : { label, value }
}

function optionLabels(options: readonly QuestionOption[]): Map<string, string> {
  const labels = new Map<string, string>()
  for (const { label, value } of options) {
    if (value !== undefined && !labels.has(value))
      labels.set(value, label)
  }
  return labels
}

/** Native answers can repeat their own option records when no request is available. */
export function antigravityQuestionAnswers(responses: unknown, questions: readonly AntigravityQuestionRecord[]): QuestionAnswer[] {
  if (!Array.isArray(responses))
    return []
  const byIndex = new Map(questions.map(record => [record.index, record]))
  const byId = new Map(questions.filter(record => record.id).map(record => [record.id, record]))
  return responses.flatMap((response, index) => {
    if (!isObject(response))
      return []
    const id = pickString(response, 'id')
    const record = byId.get(id) ?? byIndex.get(index)
    if (record?.prompt === null)
      return []
    const question = record?.prompt
    const echoed = optionLabels(Array.isArray(response.options)
      ? response.options.filter(isObject).map(questionOption)
      : [])
    const requested = optionLabels(question?.options ?? [])
    const selected = stringArray(response.selectedOptionIds).filter(option => option !== '').map(option =>
      echoed.get(option) || requested.get(option) || option)
    const answer = pickString(response, 'writeInResponse').trim() || selected.join(', ') || (response.skipped === true
      ? 'Skipped'
      : '')
    if (!answer)
      return []
    return [
      {
        header: pickString(response, 'question') || question?.question || `Question ${index + 1}`,
        answer,
      },
    ]
  })
}
