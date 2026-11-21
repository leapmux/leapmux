import type { ControlQuestion, QuestionOption, QuestionPrompt } from '../../model/question'
import type { QuestionAnswer } from '../../model/tools/question'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

export function antigravityIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickObject(pickObject(payload, 'requestedInteraction'), 'askQuestion') !== null
}

export function antigravityExtractQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const ask = pickObject(pickObject(payload, 'requestedInteraction'), 'askQuestion')
  const questions = Array.isArray(ask?.questions)
    ? ask.questions
    : []
  return questions.filter(isObject).map(question => ({
    question: pickString(question, 'question'),
    options: Array.isArray(question.options)
      ? question.options.filter(isObject).map(option => questionOption(option))
      : [],
    multiSelect: question.isMultiSelect === true,
  }))
}

function questionOption(option: Record<string, unknown>): QuestionOption {
  const label = pickString(option, 'text')
  const id = pickString(option, 'id', undefined)
  return id === undefined
    ? { label }
    : { label, value: id }
}

export function antigravityQuestionPrompts(records: unknown): QuestionPrompt[] {
  return questionsFromRecords(records, question => ({
    question: pickString(question, 'question'),
  }), (record) => {
    const option = questionOption(record)
    return option.label.trim()
      ? option
      : null
  })
}

/** Native answers can repeat their own option records when no request is available. */
export function antigravityQuestionAnswers(responses: unknown, questions: readonly QuestionPrompt[]): QuestionAnswer[] {
  if (!Array.isArray(responses))
    return []
  return responses.flatMap((response, index) => {
    if (!isObject(response))
      return []
    const question = questions[index]
    const echoedOptions = Array.isArray(response.options)
      ? response.options.filter(isObject).map(questionOption)
      : []
    const selected = stringArray(response.selectedOptionIds).filter(id => id !== '').map(id =>
      echoedOptions.find(option => option.value === id)?.label
      || question?.options.find(option => option.value === id)?.label
      || id)
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
