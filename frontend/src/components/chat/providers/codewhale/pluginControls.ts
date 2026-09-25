import type { ProviderControlCapability } from '../capabilities'
import { CODEWHALE_POSTURE } from '~/generated/contracts/codewhale-protocol'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { codewhaleAnswers, codewhaleIsQuestionRequest, codewhaleQuestionsFromPayload } from './askUserQuestion'
import { codewhaleControlResponseSummary } from './controlResponse'
import { codewhaleExtractControl } from './extractControl'

/** The complete Codewhale control channel, separate from provider registration. */
export const codewhaleControls: ProviderControlCapability = {
  controlResponseDisplay: codewhaleControlResponseSummary,
  askUserQuestion: {
    isRequest: codewhaleIsQuestionRequest,
    extractQuestions: codewhaleQuestionsFromPayload,
    // The runtime's own answer list rides under `updatedInput.answers`, so the worker
    // forwards it as it stands. See `codewhaleAnswers`.
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAllowResponse(request.requestId, {
        ...getToolInput(request.payload),
        answers: codewhaleAnswers(questions, answerState),
      })),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
  // Composer send is always a rejection. Allow lives on its own button, and an empty
  // send denies without a reason. The worker delivers a typed reason as the reader's
  // next message, because the runtime's approval route carries a decision alone.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  extractControl: codewhaleExtractControl,
  permissionPresets: {
    smart: { sets: { permissionMode: CODEWHALE_POSTURE.AutoReview } },
    bypass: { sets: { permissionMode: CODEWHALE_POSTURE.FullAccess } },
  },
}
