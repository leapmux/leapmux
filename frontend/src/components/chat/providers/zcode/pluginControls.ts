import type { ControlResponseSender } from '../../controls/types'
import type { ProviderControlCapability } from '../capabilities'
import { buildControlResponseEnvelope, buildDenyResponse, getToolInput, withControlChoice } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { zcodeIsAskUserQuestion, zcodeQuestionsFromPayload } from './askUserQuestion'
import { zcodeControlResponseSummary } from './controlResponse'
import { zcodeExtractControl } from './extractControl'
import { zcodePermissionPresets } from './permissionPresets'

/**
 * The allow-class option ids zcode's backend emits, mirrored from its own
 * fail-safe set: an id outside it answers deny, because granting a choice the
 * backend never offered is the one outcome a permission answer must not have.
 */
const ZCODE_ALLOW_OPTION_IDS = new Set(['allow', 'allow_once', 'allow_always', 'allow_project', 'allowSession'])

/** Send one offered option as the answer, with its id as the choice the worker echoes. */
export function sendZCodePermissionOption(onRespond: ControlResponseSender, requestId: string, optionId: string): Promise<void> {
  const behavior = ZCODE_ALLOW_OPTION_IDS.has(optionId) ? 'allow' : 'deny'
  return sendResponse(onRespond, withControlChoice(
    buildControlResponseEnvelope(requestId, { behavior }),
    optionId,
  ))
}

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
  sendPermissionOption: sendZCodePermissionOption,
  permissionPresets: zcodePermissionPresets,
}
