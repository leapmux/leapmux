import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'

import { sendResponse } from '../../controls/types'
import { codewhaleAnswers, codewhaleIsQuestionRequest, codewhaleQuestionsFromPayload } from './askUserQuestion'
import { codewhaleControlResponseSummary } from './controlResponse'
import { codewhaleExtractControl } from './extractControl'
import { codewhalePermissionPresets } from './permissionPresets'

/** The complete Codewhale control channel, separate from provider registration. */
export const codewhaleControls: ProviderControlCapability = {
  controlResponseDisplay: codewhaleControlResponseSummary,
  askUserQuestion: {
    isRequest: codewhaleIsQuestionRequest,
    extractQuestions: codewhaleQuestionsFromPayload,
    // The runtime's answer list appears in `updatedInput.answers`. The worker forwards it unchanged.
    // See `codewhaleAnswers`.
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAllowResponse(request.requestId, {
        ...getToolInput(request.payload),
        answers: codewhaleAnswers(questions, answerState),
      })),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
  // The composer always rejects. The Allow button owns approval. An empty send denies without a reason.
  // The worker delivers typed feedback as the user's next message because the native approval route carries only a decision.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  extractControl: codewhaleExtractControl,
  permissionPresets: codewhalePermissionPresets,
}
