import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse } from '~/utils/controlResponse'

import { sendResponse } from '../../controls/types'
import { buildDroidAnswer, droidIsQuestionRequest, droidQuestionsFromPayload } from './askUserQuestion'
import { droidControlResponseSummary } from './controlResponse'
import { droidExtractControl } from './extractControl'
import { droidPermissionPresets } from './permissionPresets'

/** The complete Factory Droid control channel, separate from provider registration. */
export const droidControls: ProviderControlCapability = {
  extractControl: droidExtractControl,
  // The saved row holds Droid's own JSON-RPC reply, not the neutral envelope.
  controlResponseDisplay: droidControlResponseSummary,
  // The composer's send is a rejection. Droid's reply cannot carry its text, so
  // the worker sends the text as the user's next message. Allow lives on its own
  // button.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // Droid's autonomy modes are the permission-mode axis. Auto (High)
  // answers every call at once, which is what Bypass means.
  permissionPresets: droidPermissionPresets,
  askUserQuestion: {
    isRequest: droidIsQuestionRequest,
    extractQuestions: droidQuestionsFromPayload,
    // The answer reads the questions from the payload, because each answer states
    // the index of its question, which the shared question model does not carry.
    // `extractQuestions` maps that same list one to one, so the positions agree.
    sendAnswer: (request, sendControlResponse, _questions, answerState) =>
      sendResponse(sendControlResponse, buildDroidAnswer(request.requestId, request.payload, answerState)),
    // A refusal is Droid's own cancel, which has no field for a reason. The worker
    // sends the reason as the user's next message.
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
}
