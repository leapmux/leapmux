import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { controlBehaviorDisplay, controlDecisionWords } from '../../persistedControlResponse'
import { claudeAskUserQuestions, claudeIsAskUserQuestion } from './askUserQuestion'
import { claudeElicitation } from './elicitation'
import { claudeExtractControl } from './extractControl'
import { claudePermissionPresets } from './permissionPresets'

/** The complete Claude control channel, separate from provider registration. */
export const claudeControls: ProviderControlCapability = {
  permissionPresets: claudePermissionPresets,
  // Claude sends the neutral behavior envelope. The shared reader therefore derives its display.
  // The request selects the plan or permission decision words that the saved row shows.
  controlResponseDisplay: withElicitationResponse(claudeElicitation, cr => controlBehaviorDisplay(
    cr.response,
    controlDecisionWords(claudeExtractControl({ payload: cr.request ?? {} })),
  )),
  askUserQuestion: {
    isRequest: claudeIsAskUserQuestion,
    extractQuestions: claudeAskUserQuestions,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
  elicitation: claudeElicitation,
  buildControlResponse(payload, content, requestId) {
    // An editor reply to a plan always rejects it with feedback. The dedicated
    // approval button owns the allow path.
    if (claudeExtractControl({ payload })?.kind === 'plan')
      return buildDenyResponse(requestId, content)
    return content
      ? buildDenyResponse(requestId, content)
      : buildAllowResponse(requestId, getToolInput(payload))
  },
  extractControl: claudeExtractControl,
}
