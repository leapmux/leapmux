import type { QuestionRequest, QuestionResult } from '../../../model/tools/question'
import type { AntigravityStep } from './toolCommon'
import { isObject, pickObject } from '~/lib/jsonPick'
import { antigravityQuestionAnswers, antigravityQuestionPrompts } from '../askUserQuestion'

export function antigravityQuestionRequest(step: AntigravityStep): QuestionRequest {
  const questions = antigravityQuestionPrompts(pickObject(step.raw, 'askQuestion')?.questions)
  return {
    questions: questions.length > 0
      ? questions
      : antigravityQuestionPrompts(step.args.questions),
  }
}

export function antigravityQuestionResult(step: AntigravityStep, request: QuestionRequest): QuestionResult | undefined {
  const interactions = step.raw.completedInteractions
  const response = Array.isArray(interactions)
    ? interactions.filter(isObject)
        .map(interaction => pickObject(pickObject(interaction, 'response'), 'askQuestion'))
        .findLast(isObject)
    : undefined
  if (response?.cancelled === true)
    return { answers: [], note: 'Dismissed' }

  const submitted = antigravityQuestionAnswers(response?.responses, request.questions)
  const answers = submitted.length > 0
    ? submitted
    : antigravityQuestionAnswers(pickObject(step.raw, 'askQuestion')?.questions, request.questions)
  return answers.length > 0
    ? { answers }
    : undefined
}
