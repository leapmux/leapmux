import type { ProviderControlCapability } from '../capabilities'
import { COPILOT_PERMISSION_MODE } from '~/generated/contracts/copilot-protocol'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { copilotControlResponseSummary } from './controlResponse'
import { copilotElicitation } from './elicitation'
import { copilotExtractControl, copilotIsQuestion, copilotQuestions } from './extractControl'
import { sendCopilotPermissionResponse } from './permissionOptions'

/** The complete Copilot control channel, separate from provider registration. */
export const copilotControls: ProviderControlCapability = {
  permissionPresets: {
    smart: { sets: { permissionMode: COPILOT_PERMISSION_MODE.Assisted } },
    bypass: { sets: { permissionMode: COPILOT_PERMISSION_MODE.AllowAll } },
  },
  controlResponseDisplay: withElicitationResponse(copilotElicitation, copilotControlResponseSummary),
  elicitation: copilotElicitation,
  // The composer's send is a rejection. Allow lives on its own button.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  extractControl: copilotExtractControl,
  sendPermissionOption: sendCopilotPermissionResponse,
  askUserQuestion: {
    isRequest: copilotIsQuestion,
    extractQuestions: copilotQuestions,
    sendAnswer: (request, sendControlResponse, _questions, answerState) => {
      const selected = answerState.selections()[0] ?? []
      const typed = answerState.customTexts()[0]?.trim() ?? ''
      // The runtime distinguishes a selected choice from a free-form answer and
      // accepts an explicit empty answer.
      const answer = selected.length > 0 ? selected.join(', ') : typed
      const response = { behavior: 'allow', answer, wasFreeform: selected.length === 0 }
      return sendResponse(sendControlResponse, buildControlResponseEnvelope(request.requestId, response))
    },
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
