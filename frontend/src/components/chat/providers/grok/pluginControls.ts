import type { ProviderAskUserQuestion } from '../capabilities'
import { grokQuestions, isGrokQuestionPayload, sendGrokQuestionRejectResponse, sendGrokQuestionResponse } from './askUserQuestion'

/** Grok Build's question dialog, `_x.ai/ask_user_question`. */
export const grokQuestionHandling: ProviderAskUserQuestion = {
  isRequest: payload => isGrokQuestionPayload(payload),
  extractQuestions: payload => grokQuestions(payload),
  sendAnswer: (request, sendControlResponse, questions, answerState) =>
    sendGrokQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
  sendReject: (request, sendControlResponse) =>
    sendGrokQuestionRejectResponse(sendControlResponse, request.requestId),
}
