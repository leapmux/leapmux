import type { ProviderPlugin } from '../capabilities'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyCopilotMessage, copilotResultDivider } from './classification'
import { copilotControlResponseSummary } from './controlResponse'
import { copilotElicitation } from './elicitation'
import { copilotExtractControl, copilotIsQuestion, copilotQuestions } from './extractControl'
import { copilotCompactionBoundary, copilotNotificationEntry } from './extractors/notification'
import { copilotExtractRow } from './extractors/row'
import { sendCopilotPermissionResponse } from './permissionOptions'
import { copilotContextUsage } from './sessionMetadata'
import { copilotRelatedMessages, copilotSpanRole } from './spanRole'

const copilotPlugin: ProviderPlugin = {
  transcript: {
    classify: classifyCopilotMessage,
    extractRow: copilotExtractRow,
    spanRole: copilotSpanRole,
    relatedMessages: copilotRelatedMessages,
    extractDivider: copilotResultDivider,
    notificationEntry: copilotNotificationEntry,
  },
  controls: {
    controlResponseDisplay: withElicitationResponse(copilotElicitation, copilotControlResponseSummary),
    elicitation: copilotElicitation,
    // The composer's own send is a rejection: Allow lives on its own button, and an
    // empty send is a refusal with no reason.
    buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
    extractControl: copilotExtractControl,
    sendPermissionOption: sendCopilotPermissionResponse,
    askUserQuestion: {
      isRequest: copilotIsQuestion,
      extractQuestions: copilotQuestions,
      sendAnswer: (request, sendControlResponse, questions, answerState) => {
        const selected = answerState.selections()[0] ?? []
        const typed = answerState.customTexts()[0]?.trim() ?? ''
        // The runtime distinguishes a typed answer from a selected choice, and it
        // accepts an explicit empty answer. Both facts travel exactly as given.
        const answer = selected.length > 0 ? selected.join(', ') : typed
        const response = { behavior: 'allow', answer, wasFreeform: selected.length === 0 }
        return sendResponse(sendControlResponse, buildControlResponseEnvelope(request.requestId, response))
      },
      sendReject: (request, sendControlResponse, message) =>
        sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
    },
    permissionPresets: {
      smart: { sets: { permissionMode: COPILOT_PERMISSION_MODE.Assisted } },
      bypass: { sets: { permissionMode: COPILOT_PERMISSION_MODE.AllowAll } },
    },
  },
  session: {
    contextUsageFromMessage: copilotContextUsage,
    compactionBoundaryFromMessage: copilotCompactionBoundary,
  },
  configuration: {
    attachments: { text: true, image: true, pdf: true, binary: true },
    // Copilot's plan axis is its SESSION mode, beside the permission mode its presets
    // drive. The two are independent, which is why the mode chip reads the session-mode
    // group and the presets write the permission-mode one.
    triggerModeGroupKey: COPILOT_OPTION.SessionMode,
    planMode: buildPlanMode(COPILOT_OPTION.SessionMode, COPILOT_MODE.Plan, COPILOT_MODE.Interactive),
  },
}

registerProvider(AgentProvider.GITHUB_COPILOT, copilotPlugin)
