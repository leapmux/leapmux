import type { ProviderControlCapability } from '../capabilities'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '~/generated/contracts/mimo-protocol'
import { buildDenyResponse } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendSelectedOptionResponse } from '../../controls/types'
import { extractOpenCodeQuestions, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from '../openCodeQuestions'
import { mimoIsQuestionRequest } from './askUserQuestion'
import { mimoControlResponseSummary } from './controlResponse'
import { mimoElicitation } from './elicitation'
import { mimoExtractControl } from './extractControl'

/** The complete MiMo control channel, separate from provider registration. */
export const mimoControls: ProviderControlCapability = {
  // An MCP server's confirmation states its answer in the elicitation words. Every
  // other answer takes MiMo's own words.
  controlResponseDisplay: withElicitationResponse(mimoElicitation, mimoControlResponseSummary),
  // MiMo's question tool is OpenCode's, so the shared OpenCode question wire reads
  // and answers it.
  askUserQuestion: {
    isRequest: mimoIsQuestionRequest,
    extractQuestions: extractOpenCodeQuestions,
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendOpenCodeQuestionResponse(sendControlResponse, request.requestId, questions, answerState),
    sendReject: (request, sendControlResponse) =>
      sendOpenCodeQuestionRejectResponse(sendControlResponse, request.requestId),
  },
  elicitation: mimoElicitation,
  // The composer sends a rejection, with the typed words as the reason. A plan the
  // reader sends back this way keeps planning with those words as its feedback.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  extractControl: mimoExtractControl,
  // A chosen option travels back as the selected-option outcome, whose id is MiMo's
  // own reply word.
  sendPermissionOption: sendSelectedOptionResponse,
  // MiMo has no permission mode. Its two runtime switches, both on, approve every
  // call, deletes included, which is what Bypass means.
  permissionPresets: { bypass: { sets: { [MIMO_OPTION.PermissionPolicy]: MIMO_PERMISSION_POLICY.Bypass } } },
}
