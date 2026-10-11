import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse } from '~/utils/controlResponse'

import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendSelectedOptionResponse } from '../../controls/types'
import { extractOpenCodeQuestions, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from '../openCodeQuestions'
import { mimoIsQuestionRequest } from './askUserQuestion'
import { mimoControlResponseSummary } from './controlResponse'
import { mimoElicitation } from './elicitation'
import { mimoExtractControl } from './extractControl'
import { mimoPermissionPresets } from './permissionPresets'

/** The complete MiMo control channel, separate from provider registration. */
export const mimoControls: ProviderControlCapability = {
  // An MCP server's confirmation states its answer in the elicitation words. Every
  // other answer takes MiMo's own words.
  controlResponseDisplay: withElicitationResponse(mimoElicitation, mimoControlResponseSummary),
  // MiMo uses OpenCode's question tool. The shared OpenCode question protocol reads and answers it.
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
  // A chosen option returns as the selected-option outcome. Its ID is MiMo's native reply word.
  sendPermissionOption: sendSelectedOptionResponse,
  // MiMo has no native permission mode. LeapMux's permission policy controls two native approval switches.
  // Bypass enables both switches and approves every call, including deletes.
  permissionPresets: mimoPermissionPresets,
}
