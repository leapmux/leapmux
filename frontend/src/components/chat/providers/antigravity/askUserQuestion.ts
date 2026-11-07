import type { ControlQuestion } from '../../model/question'
import { isObject, pickObject } from '~/lib/jsonPick'

export function antigravityIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickObject(pickObject(payload, 'requestedInteraction'), 'askQuestion') !== null
}

export function antigravityExtractQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const ask = pickObject(pickObject(payload, 'requestedInteraction'), 'askQuestion')
  const questions = Array.isArray(ask?.questions)
    ? ask.questions
    : []
  return questions.filter(isObject).map(question => ({
    question: typeof question.question === 'string'
      ? question.question
      : '',
    options: Array.isArray(question.options)
      ? question.options.filter(isObject).map(option => ({
          label: typeof option.text === 'string'
            ? option.text
            : '',
          ...(typeof option.id === 'string'
            ? { value: option.id }
            : {}),
        }))
      : [],
    multiSelect: question.isMultiSelect === true,
  }))
}
