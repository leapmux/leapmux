import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { ACP_PERMISSION_OUTCOME } from '~/generated/contracts/acp-protocol'
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

export function getCursorQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const params = getCursorParams(payload)
  if (!Array.isArray(params?.questions))
    return []
  return params.questions.filter(isObject).flatMap((question) => {
    const id = pickString(question, 'id', undefined)
    const prompt = pickString(question, 'prompt')
    // The question's own words head it; the id stands in when it states none.
    const header = prompt || id
    return [
      {
        ...(id !== undefined
          ? { id }
          : {}),
        question: prompt,
        ...(header !== undefined
          ? { header }
          : {}),
        multiSelect: question.allowMultiple === true,
        // The native ACP adapter copies selectedOptionIds but drops freeformText.
        allowCustomText: false,
        options: (Array.isArray(question.options)
          ? question.options
          : []).filter(isObject).flatMap((option) => {
          const label = pickString(option, 'label') || pickString(option, 'id')
          return label
            ? [
                { value: pickString(option, 'id') || label, label },
              ]
            : []
        }),
      },
    ]
  })
}

export async function sendCursorQuestionResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  questions: ControlQuestion[],
  answerState: ControlAnswerState,
): Promise<void> {
  // Cursor's terminal interface supports custom text. Its ACP adapter discards that field.
  // Refuse stale drafts instead of sending an answer that the model cannot read.
  const answers = questions.map((question, index) => {
    const selected = answerState.selections()[index] ?? []
    const typed = answerState.customTexts()[index]?.trim() ?? ''
    if (typed)
      throw new Error('Cursor accepts selected options only on this connection.')
    return {
      questionId: question.id || `q${index}`,
      selectedOptionIds: selected,
    }
  }).filter(answer => answer.selectedOptionIds.length > 0)

  if (answers.length !== questions.length || answers.length === 0)
    throw new Error('Choose an option for every Cursor question.')

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
        outcome: ACP_PERMISSION_OUTCOME.Cancelled,
        ...(reason
          ? { reason }
          : {}),
      },
    },
  })
}
