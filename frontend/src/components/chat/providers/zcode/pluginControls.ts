import type { ControlResponseSender } from '../../controls/types'
import type { ProviderControlCapability } from '../capabilities'
import { PROVIDER_DISPLAY_NAME } from '~/generated/contracts/providers'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildControlResponseEnvelope, buildDenyResponse, getToolInput, withControlChoice } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { withWorkspaceTrustControl, withWorkspaceTrustOptions, withWorkspaceTrustResponse } from '../workspaceTrust'
import { zcodeIsAskUserQuestion, zcodeQuestionsFromPayload } from './askUserQuestion'
import { zcodeControlResponseSummary } from './controlResponse'
import { zcodeElicitation } from './elicitation'
import { zcodeExtractControl } from './extractControl'
import { zcodePermissionPresets } from './permissionPresets'

/**
 * The allow-class option ids zcode's backend emits, mirrored from its own
 * fail-safe set: an id outside it answers deny, because granting a choice the
 * backend never offered is the one outcome a permission answer must not have.
 */
const ZCODE_ALLOW_OPTION_IDS = new Set([
  'allow',
  'allow_once',
  'allow_always',
  'allow_project',
  'allowSession',
])

/** Send one offered option as the answer, with its id as the choice the worker echoes. */
export function sendZCodePermissionOption(onRespond: ControlResponseSender, requestId: string, optionId: string): Promise<void> {
  const behavior = ZCODE_ALLOW_OPTION_IDS.has(optionId)
    ? 'allow'
    : 'deny'
  return sendResponse(onRespond, withControlChoice(
    buildControlResponseEnvelope(requestId, { behavior }),
    optionId,
  ))
}

/** The complete ZCode control channel, separate from provider registration. */
export const zcodeControls: ProviderControlCapability = {
  controlResponseDisplay: withWorkspaceTrustResponse(withElicitationResponse(zcodeElicitation, zcodeControlResponseSummary)),
  // An MCP server's form, which ZCode forwards as a user-input request. The worker stores it under its own tool name,
  // so the question reader below leaves it to this form.
  elicitation: zcodeElicitation,
  askUserQuestion: {
    isRequest: zcodeIsAskUserQuestion,
    extractQuestions: zcodeQuestionsFromPayload,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
  // The composer always rejects. Its placeholder asks for a rejection reason.
  // The Allow button owns approval. An empty send denies without a reason.
  // Claude's empty-send approval does not apply to ZCode.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // The Worker asks ZCode's hook-trust question itself (providerkit.WorkspaceTrust).
  extractControl: withWorkspaceTrustControl(zcodeExtractControl, PROVIDER_DISPLAY_NAME[AgentProvider.ZCODE] ?? 'The agent'),
  sendPermissionOption: withWorkspaceTrustOptions(sendZCodePermissionOption),
  permissionPresets: zcodePermissionPresets,
}
