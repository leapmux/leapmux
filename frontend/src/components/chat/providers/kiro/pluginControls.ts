import type { ProviderAskUserQuestion } from '../capabilities'
import { isKiroUserInputPayload, kiroUserInputRequestQuestions, sendKiroUserInputDismissal, sendKiroUserInputResponse } from './askUserQuestion'

/** Kiro's question dialog, `_kiro/userInput`. */
export const kiroQuestionHandling: ProviderAskUserQuestion = {
  isRequest: payload => isKiroUserInputPayload(payload),
  extractQuestions: payload => kiroUserInputRequestQuestions(payload),
  sendAnswer: (request, sendControlResponse, questions, answerState) =>
    sendKiroUserInputResponse(sendControlResponse, request.requestId, questions, answerState),
  sendReject: (request, sendControlResponse) =>
    sendKiroUserInputDismissal(sendControlResponse, request.requestId),
}
