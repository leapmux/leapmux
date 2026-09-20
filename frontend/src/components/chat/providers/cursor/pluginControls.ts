import type { ProviderAskUserQuestion, ProviderControlCapability } from '../capabilities'
import {
  getCursorQuestions,
  isCursorAskQuestionPayload,
  isCursorCreatePlanPayload,
  sendCursorQuestionRejectResponse,
  sendCursorQuestionResponse,
} from './askUserQuestion'
import { CursorControlActions } from './CursorControlActions'

/** Cursor's native request handlers, separate from provider registration. */
export const cursorQuestionHandling: ProviderAskUserQuestion = {
  isRequest: payload => isCursorAskQuestionPayload(payload),
  extractQuestions: payload => getCursorQuestions(payload),
  sendAnswer: (request, sendControlResponse, questions, answerState) =>
    sendCursorQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
  sendReject: (request, sendControlResponse, message) =>
    sendCursorQuestionRejectResponse(sendControlResponse, request.requestId, message),
}

export const cursorControlActionsFor: NonNullable<ProviderControlCapability['controlActionsFor']> = payload =>
  isCursorCreatePlanPayload(payload) ? CursorControlActions : undefined
