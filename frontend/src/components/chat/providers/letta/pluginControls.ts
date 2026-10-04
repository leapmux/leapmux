import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse } from '~/utils/controlResponse'

import { sendResponse } from '../../controls/types'
import { controlBehaviorDisplay } from '../../persistedControlResponse'
import { buildLettaAnswer, lettaIsQuestionRequest, lettaQuestionsFromPayload } from './askUserQuestion'
import { lettaExtractControl } from './extractControl'
import { lettaPermissionPresets } from './permissionPresets'

/** The complete Letta Code control channel, separate from provider registration. */
export const lettaControls: ProviderControlCapability = {
  extractControl: lettaExtractControl,
  controlResponseDisplay: cr => controlBehaviorDisplay(cr.response),
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // Unrestricted auto-approves every tool, which is what Bypass means.
  permissionPresets: lettaPermissionPresets,
  askUserQuestion: {
    isRequest: lettaIsQuestionRequest,
    extractQuestions: lettaQuestionsFromPayload,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildLettaAnswer(request.requestId, questions, answerState)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
