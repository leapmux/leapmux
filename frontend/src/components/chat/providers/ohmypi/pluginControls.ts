import type { ProviderControlCapability } from '../capabilities'
import { OH_MY_PI_APPROVAL_DIALOG, OH_MY_PI_APPROVAL_MODE } from '~/generated/contracts/ohmypi-protocol'
import { isOhMyPiAskRequest, isOhMyPiDialogQuestion, ohMyPiAskAnswers, ohMyPiDialogAnswer, ohMyPiQuestionsFromPayload } from './askUserQuestion'
import { isOhMyPiApproval, ohMyPiAskAnswer, ohMyPiCancelResponse, ohMyPiConfirmResponse, ohMyPiControlResponseSummary, ohMyPiValueResponse, sendOhMyPiResponse } from './controlResponse'
import { ohMyPiExtractControl } from './extractControl'

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
      // The one dialog the form answers is a select. It takes one of its own
      // options and nothing else, so no choice is a dismissal rather than an empty
      // answer omp would refuse.
      const answer = ohMyPiDialogAnswer(answerState)
      await sendOhMyPiResponse(sendControlResponse, answer.trim() ? ohMyPiValueResponse(request.requestId, answer) : ohMyPiCancelResponse(request.requestId))
    },
    // A dismissed question is omp's own cancellation. For the `ask` tool that also
    // ends omp's run: omp treats a question the reader refused as a stop.
    sendReject: (request, sendControlResponse) => sendOhMyPiResponse(sendControlResponse, ohMyPiCancelResponse(request.requestId)),
  },
  extractControl: ohMyPiExtractControl,
  // omp answers a dialog with a confirm, a value or a cancellation envelope.
  dialogResponder: {
    confirm: ohMyPiConfirmResponse,
    value: ohMyPiValueResponse,
    cancel: ohMyPiCancelResponse,
  },
  // omp's approval takes its two words as the dialog's value.
  sendPermissionOption: (onRespond, requestId, optionId) => sendOhMyPiResponse(onRespond, ohMyPiValueResponse(requestId, optionId)),
  // Typed text beside an approval is a reason to refuse, which omp's `Deny` cannot
  // carry: the approval answers `Deny`, and the text follows as the reader's next
  // message.
  buildControlResponse: (payload, _content, requestId) => isOhMyPiApproval(payload)
    ? ohMyPiValueResponse(requestId, OH_MY_PI_APPROVAL_DIALOG.Deny)
    : ohMyPiCancelResponse(requestId),
  controlFeedbackAsFollowUpMessage: isOhMyPiApproval,
  controlEditorPurpose: payload => isOhMyPiApproval(payload) ? 'feedback' : 'none',
  permissionPresets: {
    // omp's `yolo` runs every tool without asking, and it is the one mode that
    // matches the Bypass preset. omp has no mode that decides for itself which call
    // needs asking, so it offers no Smart preset.
    bypass: { sets: { permissionMode: OH_MY_PI_APPROVAL_MODE.Yolo } },
  },
}
