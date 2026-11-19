import type { QuestionAnswer } from '../../../model/tools/question'
import { isObject, pickString } from '~/lib/jsonPick'

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
