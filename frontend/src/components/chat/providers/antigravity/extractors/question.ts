import type { QuestionRequest, QuestionResult } from '../../../model/tools/question'
import type { AntigravityStep } from './toolCommon'
import { isObject, pickObject } from '~/lib/jsonPick'
import { antigravityQuestionAnswers, antigravityQuestionRecords } from '../askUserQuestion'

function questionRecords(step: AntigravityStep) {
  const records = antigravityQuestionRecords(pickObject(step.raw, 'askQuestion')?.questions)
  return records.some(record => record.prompt)
    ? records
    : antigravityQuestionRecords(step.args.questions)
}

export function antigravityQuestionRequest(step: AntigravityStep): QuestionRequest {
  return {
    questions: questionRecords(step).flatMap(({ prompt }) => prompt
      ? [
          { question: prompt.question, options: prompt.options },
        ]
      : []),
  }
}

export function antigravityQuestionResult(step: AntigravityStep): QuestionResult | undefined {
  const interactions = step.raw.completedInteractions
  const response = Array.isArray(interactions)
    ? interactions.filter(isObject)
        .map(interaction => pickObject(pickObject(interaction, 'response'), 'askQuestion'))
        .findLast(isObject)
    : undefined
  if (response?.cancelled === true)
    return { answers: [], note: 'Dismissed' }

  const records = questionRecords(step)
  const submitted = antigravityQuestionAnswers(response?.responses, records)
  const answers = submitted.length > 0
    ? submitted
    : antigravityQuestionAnswers(pickObject(step.raw, 'askQuestion')?.questions, records)
  return answers.length > 0
    ? { answers }
    : undefined
}
