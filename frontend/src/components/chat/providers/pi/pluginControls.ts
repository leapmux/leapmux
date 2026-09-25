import type { ProviderControlCapability } from '../capabilities'
import type { PiExtensionResponse } from './controlResponse'
import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { piQuestionsFromPayload } from './askUserQuestion'
import {
  piAskAnswerValue,
  piCancelResponse,
  piConfirmResponse,
  piControlResponseSummary,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'
import { piExtractControl } from './extractControl'
import { isPiMcpApproval, piMcpApproval } from './mcpApproval'
import { PiPlanApprovalActions } from './PiPlanApprovalActions'
import { isPiPlanApproval } from './planRequest'

/** The complete Pi control channel, separate from provider registration. */
export const piControls: ProviderControlCapability = {
  controlResponseDisplay: piControlResponseSummary,
  elicitation: piMcpApproval,
  askUserQuestion: {
    isRequest: payload => payload.type === PI_EVENT.ExtensionUIRequest
      && (payload.method === PI_DIALOG_METHOD.Input || payload.method === PI_DIALOG_METHOD.Select)
      && !isPiPlanApproval(payload)
      && !isPiMcpApproval(payload),
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
        // Editor content is a denial reason, but `confirm` can carry only true or
        // false. An empty reply confirms and any content denies.
        response = piConfirmResponse(requestId, content.trim() === '')
        break
      case PI_DIALOG_METHOD.Input:
      case PI_DIALOG_METHOD.Editor:
        // Pi distinguishes an empty submitted value from cancellation.
        response = piValueResponse(requestId, content)
        break
      case PI_DIALOG_METHOD.Select:
        // Without a typed reply, treat the response as cancellation.
        response = content.trim() ? piValueResponse(requestId, content) : piCancelResponse(requestId)
        break
      default:
        response = piCancelResponse(requestId)
    }
    return response
  },
  extractControl: piExtractControl,
  // Pi answers a dialog with a confirm, a value or a cancellation envelope.
  dialogResponder: {
    confirm: piConfirmResponse,
    value: piValueResponse,
    cancel: piCancelResponse,
  },
  // Pi's plan menu answers with its own action words, which no shared row states.
  controlActionsFor: payload => isPiPlanApproval(payload) ? PiPlanApprovalActions : undefined,
}
