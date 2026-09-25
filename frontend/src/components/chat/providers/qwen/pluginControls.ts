import type { ProviderAskUserQuestion } from '../capabilities'
import { isQwenQuestionPayload, qwenQuestions, sendQwenQuestionRejectResponse, sendQwenQuestionResponse } from './askUserQuestion'

/** Qwen Code's question dialog, a permission request that its `_meta` marks as one. */
export const qwenQuestionHandling: ProviderAskUserQuestion = {
  isRequest: payload => isQwenQuestionPayload(payload),
  extractQuestions: payload => qwenQuestions(payload),
  sendAnswer: (request, sendControlResponse, questions, answerState) =>
    sendQwenQuestionResponse(sendControlResponse, request.requestId, request.payload, questions, answerState),
  sendReject: (request, sendControlResponse) =>
    sendQwenQuestionRejectResponse(sendControlResponse, request.requestId, request.payload),
}
