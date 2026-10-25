import type { ProviderControlCapability } from '../capabilities'
import { PROVIDER_DISPLAY_NAME } from '~/generated/contracts/providers'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/utils/controlResponse'

import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { withWorkspaceTrustControl, withWorkspaceTrustOptions, withWorkspaceTrustResponse } from '../workspaceTrust'
import { copilotControlResponseSummary } from './controlResponse'
import { copilotElicitation } from './elicitation'
import { copilotExtractControl, copilotIsQuestion, copilotQuestions } from './extractControl'
import { sendCopilotPermissionResponse } from './permissionOptions'
import { copilotPermissionPresets } from './permissionPresets'

/** The complete Copilot control channel, separate from provider registration. */
export const copilotControls: ProviderControlCapability = {
  permissionPresets: copilotPermissionPresets,
  controlResponseDisplay: withWorkspaceTrustResponse(withElicitationResponse(copilotElicitation, copilotControlResponseSummary)),
  elicitation: copilotElicitation,
  // The composer's send is a rejection. Allow lives on its own button.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // The Worker asks Copilot's folder-trust question itself (providerkit.WorkspaceTrust).
  extractControl: withWorkspaceTrustControl(copilotExtractControl, PROVIDER_DISPLAY_NAME[AgentProvider.GITHUB_COPILOT] ?? 'The agent'),
  sendPermissionOption: withWorkspaceTrustOptions(sendCopilotPermissionResponse),
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
