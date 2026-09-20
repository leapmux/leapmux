import type { ProviderControlCapability } from '../capabilities'
import { ZCODE_MODE } from '~/generated/contracts/zcode-protocol'
import { buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { zcodeIsAskUserQuestion, zcodeQuestionsFromPayload } from './askUserQuestion'
import { zcodeControlResponseSummary } from './controlResponse'
import { zcodeExtractControl } from './extractControl'

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
  // Composer send is always a rejection. The placeholder asks for a rejection
  // reason, Allow lives on its own button, and an empty send denies without a
  // reason. Claude's empty-send-is-allow behavior does not apply.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  extractControl: zcodeExtractControl,
  permissionPresets: { bypass: { sets: { permissionMode: ZCODE_MODE.Yolo } } },
}
