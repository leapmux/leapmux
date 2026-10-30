import type { ProviderAskUserQuestion } from '../capabilities'
import { NATIVE_QUESTION_EVENT } from '~/generated/contracts/native-questions'
import { extractNativeQuestions, sendNativeQuestionRejectResponse, sendNativeQuestionResponse } from '../nativeQuestions'

export const kiloQuestionHandling: ProviderAskUserQuestion = {
  isRequest: payload => payload?.type === NATIVE_QUESTION_EVENT.QuestionAsked,
  extractQuestions: extractNativeQuestions,
  sendAnswer: (request, sendControlResponse, questions, answerState) => sendNativeQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
  sendReject: (request, sendControlResponse) => sendNativeQuestionRejectResponse(sendControlResponse, request.requestId),
}
