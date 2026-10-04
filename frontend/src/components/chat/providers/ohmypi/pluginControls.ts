import type { ProviderControlCapability } from '../capabilities'
import { OH_MY_PI_APPROVAL_DIALOG } from '~/generated/contracts/ohmypi-protocol'
import { isOhMyPiAskRequest, isOhMyPiDialogQuestion, ohMyPiAskAnswers, ohMyPiDialogAnswer, ohMyPiQuestionsFromPayload } from './askUserQuestion'
import { isOhMyPiApproval, ohMyPiAskAnswer, ohMyPiCancelResponse, ohMyPiConfirmResponse, ohMyPiControlResponseSummary, ohMyPiValueResponse, sendOhMyPiResponse } from './controlResponse'
import { ohMyPiExtractControl } from './extractControl'
import { ohMyPiPermissionPresets } from './permissionPresets'

/** The complete omp control channel, separate from provider registration. */
export const ohMyPiControls: ProviderControlCapability = {
  controlResponseDisplay: ohMyPiControlResponseSummary,
  askUserQuestion: {
    isRequest: payload => isOhMyPiAskRequest(payload) || isOhMyPiDialogQuestion(payload),
    extractQuestions: ohMyPiQuestionsFromPayload,
    async sendAnswer(request, sendControlResponse, questions, answerState) {
      if (isOhMyPiAskRequest(request.payload)) {
        await sendOhMyPiResponse(sendControlResponse, ohMyPiAskAnswer(request.requestId, ohMyPiAskAnswers(questions, answerState)))
        return
      }
      // This form answers only a select dialog. The dialog accepts one of its own options.
      // When the user selects no option, the handler dismisses the dialog. An empty answer would fail.
      const answer = ohMyPiDialogAnswer(answerState)
      await sendOhMyPiResponse(sendControlResponse, answer.trim() ? ohMyPiValueResponse(request.requestId, answer) : ohMyPiCancelResponse(request.requestId))
    },
    // A dismissed question sends omp's native cancellation. The `ask` tool then ends omp's run because a refused question stops it.
    sendReject: (request, sendControlResponse) => sendOhMyPiResponse(sendControlResponse, ohMyPiCancelResponse(request.requestId)),
  },
  extractControl: ohMyPiExtractControl,
  // omp answers a dialog with one envelope:
  // - Confirmation.
  // - A value.
  // - Cancellation.
  dialogResponder: {
    confirm: ohMyPiConfirmResponse,
    value: ohMyPiValueResponse,
    cancel: ohMyPiCancelResponse,
  },
  // omp's approval takes its two words as the dialog's value.
  sendPermissionOption: (onRespond, requestId, optionId) => sendOhMyPiResponse(onRespond, ohMyPiValueResponse(requestId, optionId)),
  // Typed feedback refuses an approval. omp's `Deny` carries no reason.
  // The approval answers `Deny`. The feedback then follows as the user's next message.
  buildControlResponse: (payload, _content, requestId) => isOhMyPiApproval(payload)
    ? ohMyPiValueResponse(requestId, OH_MY_PI_APPROVAL_DIALOG.Deny)
    : ohMyPiCancelResponse(requestId),
  controlFeedbackAsFollowUpMessage: isOhMyPiApproval,
  controlEditorPurpose: payload => isOhMyPiApproval(payload) ? 'feedback' : 'none',
  permissionPresets: ohMyPiPermissionPresets,
}
