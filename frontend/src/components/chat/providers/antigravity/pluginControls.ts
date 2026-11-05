import type { ProviderControlCapability } from '../capabilities'
import { buildDenyResponse } from '~/utils/controlResponse'
import { antigravityExtractQuestions, antigravityIsQuestionRequest } from './askUserQuestion'
import { antigravityControlResponseSummary, antigravityPermissionResponse, sendAntigravityPermissionResponse, sendAntigravityQuestionReject, sendAntigravityQuestionResponse } from './controlResponse'
import { antigravityExtractControl } from './extractControl'
import { antigravityPermissionPresets } from './permissionPresets'

export const antigravityControls: ProviderControlCapability = {
  extractControl: antigravityExtractControl,
  controlResponseDisplay: antigravityControlResponseSummary,
  askUserQuestion: {
    isRequest: antigravityIsQuestionRequest,
    extractQuestions: antigravityExtractQuestions,
    sendAnswer: sendAntigravityQuestionResponse,
    sendReject: (request, sendControlResponse) => sendAntigravityQuestionReject(request, sendControlResponse),
  },
  buildControlResponse: (payload, content, requestId) => antigravityExtractControl({ payload })?.kind === 'plan'
    ? buildDenyResponse(requestId, content)
    : antigravityPermissionResponse(requestId, 'reject', content),
  sendPermissionOption: sendAntigravityPermissionResponse,
  permissionPresets: antigravityPermissionPresets,
}
