import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse, getToolInput } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { zcodeIsAskUserQuestion, zcodeQuestionsFromPayload } from './askUserQuestion'
import { zcodeControlResponseSummary } from './controlResponse'
import { zcodeExtractControl } from './extractControl'
import { zcodePermissionPresets } from './permissionPresets'

/** The complete ZCode control channel, separate from provider registration. */
export const zcodeControls: ProviderControlCapability = {
  controlResponseDisplay: zcodeControlResponseSummary,
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
  extractControl: zcodeExtractControl,
  permissionPresets: zcodePermissionPresets,
}
