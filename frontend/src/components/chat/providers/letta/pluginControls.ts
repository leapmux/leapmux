import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { lettaIsQuestionRequest, lettaQuestionsFromPayload, lettaQuestionToolInput } from './askUserQuestion'
import { lettaControlResponseSummary } from './controlResponse'
import { lettaExtractControl } from './extractControl'
import { lettaPermissionPresets } from './permissionPresets'

/** The complete Letta Code control channel, separate from provider registration. */
export const lettaControls: ProviderControlCapability = {
  extractControl: lettaExtractControl,
  // The stored row holds what the Worker sent to Letta Code: an approval_response
  // for a permission, and the question response for a question. Neither is the
  // neutral envelope that the browser sent.
  controlResponseDisplay: lettaControlResponseSummary,
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // Unrestricted auto-approves every tool, which is what Bypass means.
  permissionPresets: lettaPermissionPresets,
  askUserQuestion: {
    isRequest: lettaIsQuestionRequest,
    extractQuestions: lettaQuestionsFromPayload,
    // The shared answer folds the picks into the input of the question call. The
    // Worker reads them there and writes the response that Letta Code reads.
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, lettaQuestionToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
