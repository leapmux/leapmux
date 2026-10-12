import type { ControlResponseSender } from '../../controls/types'
import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildControlResponseEnvelope, buildDenyResponse, getToolInput, withControlChoice } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendResponse } from '../../controls/types'
import { controlBehaviorDisplay, controlDecisionWords } from '../../persistedControlResponse'
import { qoderAskUserQuestions, qoderIsAskUserQuestion } from './askUserQuestion'
import { qoderElicitation } from './elicitation'
import { qoderExtractControl } from './extractControl'
import { qoderPermissionPresets } from './permissionPresets'

/**
 * Send one offered scope option. Both offered ids are allows -- the footer's
 * Allow button submits whichever scope pill is selected -- and the choice rides
 * the neutral envelope for the worker to translate: `session` becomes Qoder's
 * `permissionScope`. An id outside the offered set answers deny, because
 * granting a choice the runtime never offered is the one outcome a permission
 * answer must not have.
 */
export function sendQoderPermissionOption(onRespond: ControlResponseSender, requestId: string, optionId: string): Promise<void> {
  const behavior = optionId === 'once' || optionId === 'session' ? 'allow' : 'deny'
  return sendResponse(onRespond, withControlChoice(
    buildControlResponseEnvelope(requestId, { behavior }),
    optionId,
  ))
}

/**
 * The Qoder control channel.
 *
 * The browser sends the neutral behavior envelope and the worker translates it
 * into Qoder's own decision object, so the shared Allow/Deny pair is the
 * surface.
 *
 * Auto approves calls that Qoder finds safe and asks about the rest. It is
 * the Smart preset. Don't Ask denies calls instead of bypassing prompts.
 */
export const qoderControls: ProviderControlCapability = {
  permissionPresets: qoderPermissionPresets,
  controlResponseDisplay: withElicitationResponse(qoderElicitation, record => controlBehaviorDisplay(
    record.response,
    controlDecisionWords(qoderExtractControl({ payload: record.request ?? {} })),
  )),
  elicitation: qoderElicitation,
  askUserQuestion: {
    isRequest: qoderIsAskUserQuestion,
    extractQuestions: qoderAskUserQuestions,
    // The reply is the neutral allow with the whole tool input plus `answers`,
    // which the worker folds into Qoder's `updatedInput`.
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
  buildControlResponse(payload, content, requestId) {
    // An editor reply to a plan always rejects it with feedback. The dedicated
    // approval button owns the allow path.
    if (qoderExtractControl({ payload })?.kind === 'plan')
      return buildDenyResponse(requestId, content)
    return content
      ? buildDenyResponse(requestId, content)
      : buildAllowResponse(requestId, getToolInput(payload))
  },
  extractControl: qoderExtractControl,
  sendPermissionOption: sendQoderPermissionOption,
}
