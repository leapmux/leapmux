import type { ProviderControlCapability } from '../capabilities'
import { DROID_MODE } from '~/generated/contracts/droid-protocol'
import { buildDenyResponse } from '~/utils/controlResponse'
import { sendResponse } from '../../controls/types'
import { controlBehaviorDisplay } from '../../persistedControlResponse'
import { buildDroidAnswer, droidIsQuestionRequest, droidQuestionsFromPayload } from './askUserQuestion'
import { droidExtractControl } from './extractControl'

/** The complete Factory Droid control channel, separate from provider registration. */
export const droidControls: ProviderControlCapability = {
  extractControl: droidExtractControl,
  controlResponseDisplay: cr => controlBehaviorDisplay(cr.response),
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // Droid's four autonomy modes are the permission-mode axis. Auto (High)
  // answers every call at once, which is what Bypass means.
  permissionPresets: { bypass: { sets: { permissionMode: DROID_MODE.AutoHigh } } },
  askUserQuestion: {
    isRequest: droidIsQuestionRequest,
    extractQuestions: droidQuestionsFromPayload,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildDroidAnswer(request.requestId, questions, answerState)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
