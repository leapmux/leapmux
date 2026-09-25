import type { ProviderControlCapability } from '../capabilities'
import { CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { buildDenyResponse } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { buildClineAnswer, clineIsQuestionRequest, clineQuestionsFromPayload } from './askUserQuestion'
import { clineControlResponseSummary } from './controlResponse'
import { clineExtractControl } from './extractControl'

/** The complete Cline control channel, separate from provider registration. */
export const clineControls: ProviderControlCapability = {
  extractControl: clineExtractControl,
  controlResponseDisplay: clineControlResponseSummary,
  // The composer's send is a refusal, and its text rides the refusal as the reason
  // Cline hands the model. Allow lives on its own button.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // Auto-approve answers every call at once, which is what Bypass means. No mode of
  // Cline's asks for the risky calls alone, so Cline offers no Smart preset.
  permissionPresets: { bypass: { sets: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } } },
  askUserQuestion: {
    isRequest: clineIsQuestionRequest,
    extractQuestions: clineQuestionsFromPayload,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildClineAnswer(request.requestId, questions, answerState)),
    // A refusal ends the question, and its text reaches the model as the tool's error.
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
