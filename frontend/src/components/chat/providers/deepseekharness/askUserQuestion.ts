import type { ControlQuestion } from '../../model/question'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export function deepseekHarnessQuestionRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  const request = pickObject(payload, 'request') ?? payload
  return Array.isArray(request.questions)
    ? request.questions.filter(isObject)
    : []
}

export function deepseekHarnessQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  return deepseekHarnessQuestionRecords(payload).flatMap((record) => {
    const id = pickString(record, 'id')
    const question = pickString(record, 'question')
    if (!id || !question)
      return []
    const header = pickString(record, 'header')
    const options = Array.isArray(record.options)
      ? record.options.filter(isObject)
      : []
    return [
      {
        id,
        question,
        options: options.flatMap((option) => {
          const label = pickString(option, 'label')
          const description = pickString(option, 'description')
          return label
            ? [
                {
                  label,
                  value: label,
                  ...(description
                    ? { description }
                    : {}),
                },
              ]
            : []
        }),
        ...(header
          ? { header }
          : {}),
        ...(record.multiSelect === true
          ? { multiSelect: true }
          : {}),
      },
    ]
  })
}
