import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'

import { buildAskAnswers } from '../../controls/AskUserQuestionControl'
import { sendResponse } from '../../controls/types'
import { codebuddyAskUserQuestions, codebuddyIsAskUserQuestion } from './askUserQuestion'
import { codebuddyControlResponseSummary } from './controlResponse'
import { codebuddyExtractControl } from './extractControl'
import { codebuddyPermissionPresets } from './permissionPresets'

/**
 * The CodeBuddy control channel.
 *
 * The answer the worker sends is CodeBuddy's own `{"allowed":true}`, so the
 * shared Allow/Deny envelope is what the browser sends and the worker
 * translates. The options stay empty for the same reason: the shared pair is
 * the surface the reader answers. The saved row holds the translated answer, so
 * the display reads CodeBuddy's own fields back.
 */
export const codebuddyControls: ProviderControlCapability = {
  permissionPresets: codebuddyPermissionPresets,
  controlResponseDisplay: codebuddyControlResponseSummary,
  askUserQuestion: {
    isRequest: codebuddyIsAskUserQuestion,
    extractQuestions: codebuddyAskUserQuestions,
    // The reply is the neutral allow with the whole tool input plus `answers`,
    // which the worker folds into CodeBuddy's `updatedInput`.
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendResponse(sendControlResponse, buildAskAnswers(answerState, questions, getToolInput(request.payload), request.requestId)),
    sendReject: (request, sendControlResponse, message) =>
      sendResponse(sendControlResponse, buildDenyResponse(request.requestId, message)),
  },
  buildControlResponse(payload, content, requestId) {
    // An editor reply to a plan always rejects it with feedback. The dedicated
    // approval button owns the allow path.
    if (codebuddyExtractControl({ payload })?.kind === 'plan')
      return buildDenyResponse(requestId, content)
    return content
      ? buildDenyResponse(requestId, content)
      : buildAllowResponse(requestId, getToolInput(payload))
  },
  extractControl: codebuddyExtractControl,
}
