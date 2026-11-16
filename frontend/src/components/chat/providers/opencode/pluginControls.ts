import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { sendSelectedOptionResponse } from '../../controls/types'
import { openCodePermissionPresets } from '../openCodePermissionPresets'
import { openCodeQuestionHandling } from './askUserQuestion'
import { openCodeControlResponseSummary } from './controlResponse'
import { openCodeElicitation } from './elicitation'
import { openCodeExtractControl } from './extractControl'

export const openCodeControls: ProviderControlCapability = {
  askUserQuestion: openCodeQuestionHandling,
  elicitation: openCodeElicitation,
  extractControl: openCodeExtractControl,
  sendPermissionOption: sendSelectedOptionResponse,
  permissionPresets: openCodePermissionPresets,
  controlResponseDisplay: withElicitationResponse(openCodeElicitation, openCodeControlResponseSummary),
  buildControlResponse: (_payload, content, requestId) => content.trim()
    ? buildDenyResponse(requestId, content)
    : buildAllowResponse(requestId, {}),
}
