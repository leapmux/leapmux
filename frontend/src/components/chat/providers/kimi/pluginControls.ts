import type { ProviderControlCapability } from '../capabilities'
import { KIMI_MODE } from '~/generated/contracts/kimi-protocol'
import { buildDenyResponse } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { buildKimiAnswers, kimiIsQuestionRequest, kimiQuestionsFromPayload } from './askUserQuestion'
import { kimiControlResponseSummary } from './controlResponse'
import { kimiExtractControl, sendKimiPermissionOption } from './extractControl'

/** The complete Kimi Code control channel, separate from provider registration. */
export const kimiControls: ProviderControlCapability = {
  // Smart runs routine edits and commands and asks for the risky ones; Bypass runs
  // everything and decides every request itself. Kimi calls the two Ask When Needed
  // and Never Ask.
  permissionPresets: {
    smart: { sets: { permissionMode: KIMI_MODE.Yolo } },
    bypass: { sets: { permissionMode: KIMI_MODE.Auto } },
  },
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
