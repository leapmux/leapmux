import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse } from '~/utils/controlResponse'

import { sendResponse } from '../../controls/types'
import { buildKimiAnswers, kimiIsQuestionRequest, kimiQuestionsFromPayload } from './askUserQuestion'
import { kimiControlResponseSummary } from './controlResponse'
import { kimiExtractControl, sendKimiPermissionOption } from './extractControl'
import { kimiPermissionPresets } from './permissionPresets'

/** The complete Kimi Code control channel, separate from provider registration. */
export const kimiControls: ProviderControlCapability = {
  // Smart runs routine edits and commands, then asks before risky calls. Bypass runs every call and decides every request itself.
  // Kimi calls these choices Ask When Needed and Never Ask.
  permissionPresets: kimiPermissionPresets,
  controlResponseDisplay: kimiControlResponseSummary,
  extractControl: kimiExtractControl,
  sendPermissionOption: sendKimiPermissionOption,
  // The composer's send is a refusal, and its text rides the refusal as the reason the
  // server hands the model. Allow lives on its own button.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  askUserQuestion: {
    isRequest: kimiIsQuestionRequest,
    extractQuestions: kimiQuestionsFromPayload,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildKimiAnswers(request.requestId, questions, answerState)),
    // A refusal dismisses the question. Its text cannot ride the dismissal, so the
    // worker sends it as the user's next message.
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
