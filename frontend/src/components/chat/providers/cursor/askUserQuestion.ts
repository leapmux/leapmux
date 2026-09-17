import type { ControlAnswerState, ControlResponseSender, Question } from '../../controls/types'
import { CURSOR_METHOD } from '~/generated/contracts/cursor-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { sendResponse } from '../../controls/types'

function getCursorParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params', undefined)
}

export function isCursorAskQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.method === CURSOR_METHOD.AskQuestion
}

export function isCursorCreatePlanPayload(payload: Record<string, unknown>): boolean {
  return payload.method === CURSOR_METHOD.CreatePlan
}

export function getCursorQuestions(payload: Record<string, unknown>): Question[] {
  const params = getCursorParams(payload)
  if (!Array.isArray(params?.questions))
    return []
  return params.questions.filter(isObject).flatMap((question) => {
    const id = pickString(question, 'id', undefined)
    const prompt = pickString(question, 'prompt')
    // The question's own words head it; the id stands in when it states none.
    const header = prompt || id
    return [{
      ...(id !== undefined ? { id } : {}),
      question: prompt,
      ...(header !== undefined ? { header } : {}),
      multiSelect: question.allowMultiple === true,
      options: (Array.isArray(question.options) ? question.options : []).filter(isObject).flatMap((option) => {
        const label = pickString(option, 'label') || pickString(option, 'id')
        return label ? [{ value: pickString(option, 'id') || label, label }] : []
      }),
    }]
  })
}

export function sendCursorQuestionResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  questions: Question[],
  answerState: ControlAnswerState,
): Promise<void> {
  // Cursor's own answer carries `freeformText` beside `selectedOptionIds`, and its own
  // interface reads both, so a typed answer travels rather than being dropped. A question
  // answered with typed text ALONE is still an answer, which is why the survival test
  // below reads either half. See RL-006.
  const answers = questions.map((question, index) => {
    const selected = answerState.selections()[index] ?? []
    const typed = answerState.customTexts()[index]?.trim() ?? ''
    return {
      questionId: question.id || `q${index}`,
      selectedOptionIds: selected,
      ...(typed ? { freeformText: typed } : {}),
    }
  }).filter(answer => answer.selectedOptionIds.length > 0 || answer.freeformText !== undefined)

  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: requestId,
    result: {
      outcome: {
        outcome: 'answered',
        answers,
      },
    },
  })
}

export function sendCursorQuestionRejectResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  reason?: string,
): Promise<void> {
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: requestId,
    result: {
      outcome: {
        outcome: 'cancelled',
        ...(reason ? { reason } : {}),
      },
    },
  })
}
