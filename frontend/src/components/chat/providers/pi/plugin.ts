import type { ProviderPlugin } from '../capabilities'
import type { PiExtensionResponse } from './controlResponse'
import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickString } from '~/lib/jsonPick'
import { registerProvider } from '../registry'
import { piQuestionsFromPayload } from './askUserQuestion'
import { classifyPiMessage } from './classification'
import {
  piAskAnswerValue,
  piCancelResponse,
  piConfirmResponse,
  piControlResponseSummary,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'
import { piExtractControl } from './extractControl'
import { piCompactionBoundary, piNotificationEntry } from './extractors/notification'
import { piResultDivider } from './extractors/resultDivider'
import { piExtractRow } from './extractors/row'
import { isPiMcpApproval, piMcpApproval } from './mcpApproval'
import { PiControlActions } from './PiControlActions'
import { isPiPlanApproval } from './planRequest'
import { resolvePiMessage } from './resolveMessage'
import { piValidateResumeHandle } from './resumeHandle'
import { piContextUsageFromMessage } from './sessionMetadata'
import { piRelatedMessages, piSpanRole } from './spanRole'

const piPlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolvePiMessage,
    spanRole: piSpanRole,
    relatedMessages: piRelatedMessages,
    classify: classifyPiMessage,
    extractRow: piExtractRow,
    // The sole Pi notification seam: consulted by the shared thread reader for each
    // message (a standalone notification or one entry of a consolidated wrapper),
    // so a multi-event thread yields every entry, not just the first.
    notificationEntry: piNotificationEntry,
    extractDivider: piResultDivider,
  },
  controls: {
    controlResponseDisplay: piControlResponseSummary,
    elicitation: piMcpApproval,
    askUserQuestion: {
      isRequest: payload => payload.type === PI_EVENT.ExtensionUIRequest
        && (payload.method === PI_DIALOG_METHOD.Input || payload.method === PI_DIALOG_METHOD.Select) && !isPiPlanApproval(payload) && !isPiMcpApproval(payload),
      extractQuestions: piQuestionsFromPayload,
      async sendAnswer(request, sendControlResponse, questions, answerState) {
        const method = pickString(request.payload, 'method')
        if (method === PI_DIALOG_METHOD.Select) {
          const value = piAskAnswerValue(answerState, questions, request.payload)
          const response = value.trim() ? piValueResponse(request.requestId, value) : piCancelResponse(request.requestId)
          await sendPiExtensionResponse(sendControlResponse, response)
          return
        }
        const text = piAskAnswerValue(answerState, questions, request.payload)
        await sendPiExtensionResponse(sendControlResponse, piValueResponse(request.requestId, text))
      },
      sendReject: (request, sendControlResponse) =>
        sendPiExtensionResponse(sendControlResponse, piCancelResponse(request.requestId)),
    },
    controlFeedbackAsFollowUpMessage: isPiPlanApproval,
    controlEditorPurpose: payload => isPiPlanApproval(payload) ? 'feedback' : 'none',
    buildControlResponse(payload, content, requestId) {
      if (isPiPlanApproval(payload))
        return piValueResponse(requestId, PI_PLAN_ACTION.Stay)
      const method = pickString(payload, 'method')
      let response: PiExtensionResponse
      switch (method) {
        case PI_DIALOG_METHOD.Confirm:
          // Editor content is interpreted as a feedback message — but `confirm`
          // can only be true/false. If the user typed a deny reason, treat any
          // content as a deny.
          response = piConfirmResponse(requestId, content.trim() === '')
          break
        case PI_DIALOG_METHOD.Input:
        case PI_DIALOG_METHOD.Editor:
          // Pi distinguishes an empty submitted value from cancellation.
          response = piValueResponse(requestId, content)
          break
        case PI_DIALOG_METHOD.Select:
          // Without a typed reply, treat as cancellation.
          response = content.trim() ? piValueResponse(requestId, content) : piCancelResponse(requestId)
          break
        default:
          response = piCancelResponse(requestId)
      }
      return response
    },
    extractControl: piExtractControl,
    // Pi answers a dialog with one of three envelopes of its own -- a confirm, a
    // value or a cancel -- and its plan approval sends an action WORD. None of the
    // three is a permission decision.
    controlActionsFor: () => PiControlActions,
  },
  session: {
    contextUsageFromMessage: piContextUsageFromMessage,
    compactionBoundaryFromMessage: piCompactionBoundary,
    // Pi's agentSessionId is a .jsonl session-file path, so the UI shortens it for
    // display and labels the copy action "session file path".
    sessionIdIsFilePath: true,
    validateResumeHandle: piValidateResumeHandle,
  },
  configuration: {
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
  },
}

registerProvider(AgentProvider.PI, piPlugin)
