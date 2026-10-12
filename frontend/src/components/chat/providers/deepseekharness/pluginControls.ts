import type { ProviderControlCapability } from '../capabilities'
import { pickObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { buildDeepseekHarnessAnswers, deepseekHarnessIsQuestionRequest, deepseekHarnessQuestions } from './askUserQuestion'
import { deepseekHarnessControlResponseSummary } from './controlResponse'
import { deepseekHarnessExtractControl } from './extractControl'
import { deepseekHarnessPermissionPresets } from './permissionPresets'

export const deepseekHarnessControls: ProviderControlCapability = {
  permissionPresets: deepseekHarnessPermissionPresets,
  controlResponseDisplay: deepseekHarnessControlResponseSummary,
  extractControl: deepseekHarnessExtractControl,
  controlToolSpanId: payload => pickString(pickObject(payload, 'request'), 'callId')
    || pickString(pickObject(payload, 'request'), 'tool_use_id'),
  sendPermissionOption: (sendControlResponse, requestId, optionId) => sendResponse(sendControlResponse, optionId === 'allow' ? buildControlResponseEnvelope(requestId, { behavior: 'allow' }) : buildDenyResponse(requestId)),
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  controlFeedbackAsFollowUpMessage: () => true,
  preservesSelectionNotes: true,
  askUserQuestion: {
    isRequest: deepseekHarnessIsQuestionRequest,
    extractQuestions: deepseekHarnessQuestions,
    sendAnswer: (request, sendControlResponse, questions, state) => sendResponse(sendControlResponse, buildDeepseekHarnessAnswers(request.requestId, questions, state)),
    sendReject: (request, sendControlResponse, message) => sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
