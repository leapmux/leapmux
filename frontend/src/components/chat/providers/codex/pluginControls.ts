import type { ProviderControlCapability } from '../capabilities'
import { CODEX_BYPASS_SETTINGS } from '~/generated/contracts/codex-bypass'
import { pickObject, pickString } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { buildJsonRpcResult, questionsFromWire } from '../../controls/types'
import { CodexControlActions } from './CodexControlActions'
import {
  codexControlResponseSummary,
  codexRequestedPermissions,
  resolveCodexDecisions,
  sendCodexUserInputRejectResponse,
  sendCodexUserInputResponse,
} from './controlResponse'
import { codexElicitation } from './elicitation'
import { codexExtractControl, isCodexPlanModePrompt } from './extractControl'

/** The complete Codex control channel, separate from provider registration. */
export const codexControls: ProviderControlCapability = {
  permissionPresets: { bypass: CODEX_BYPASS_SETTINGS },
  preservesSelectionNotes: true,
  controlResponseDisplay: withElicitationResponse(codexElicitation, codexControlResponseSummary),
  askUserQuestion: {
    isRequest: payload => payload.method === 'item/tool/requestUserInput',
    // Read every question element through the shared validator. A valid outer
    // array does not make a null or string element a question.
    extractQuestions: payload => questionsFromWire(pickObject(payload, 'params')?.questions),
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendCodexUserInputResponse(sendControlResponse, request.requestId, questions, answerState),
    sendReject: (request, sendControlResponse) =>
      sendCodexUserInputRejectResponse(sendControlResponse, request.requestId),
  },
  elicitation: codexElicitation,
  buildControlResponse(payload, content, requestId) {
    const method = pickString(payload, 'method', '')
    if (isCodexPlanModePrompt(payload))
      return content ? buildDenyResponse(requestId, content) : buildAllowResponse(requestId, getToolInput(payload))
    if (method === 'item/permissions/requestApproval') {
      return buildJsonRpcResult(requestId, {
        permissions: content ? {} : codexRequestedPermissions(payload),
        scope: 'turn',
      })
    }
    const decisions = resolveCodexDecisions(pickObject(payload, 'params')?.availableDecisions)
    // This path must answer because the pending request blocks the agent. It uses
    // the canonical token of the requested polarity when the wire offered no
    // matching decision. The banner can omit a missing choice; this path cannot
    // omit the reply.
    const decision = content
      ? decisions.negative ?? 'cancel'
      : decisions.positive ?? 'accept'
    return buildJsonRpcResult(requestId, { decision })
  },
  // The worker already forwards synthetic plan feedback as a user message.
  controlFeedbackAsFollowUpMessage: payload => !isCodexPlanModePrompt(payload),
  extractControl: codexExtractControl,
  controlActionsFor: () => CodexControlActions,
}
