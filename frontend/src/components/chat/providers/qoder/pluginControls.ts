import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { qoderAskUserQuestions, qoderIsAskUserQuestion } from './askUserQuestion'
import { qoderExtractControl } from './extractControl'

/**
 * The Qoder control channel.
 *
 * The browser sends the neutral behavior envelope and the worker translates it
 * into Qoder's own decision object, so the shared Allow/Deny pair is the
 * surface.
 *
 * There is no permission preset: Qoder's `dontAsk` auto-DENIES what is not
 * pre-approved and `auto` still asks, so neither is a bypass mode and the
 * shortcut stays absent.
 */
export const qoderControls: ProviderControlCapability = {
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
}
