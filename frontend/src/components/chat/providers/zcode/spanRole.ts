import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type {} from '../registry'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeControlPlanText } from './extractors/plan'
import { zcodeEnvelope, zcodeExtractTool, zcodeToolSpanRole } from './extractors/toolCommon'

/**
 * ZCode span role. The `tool.updated` KIND discriminates the request from the result,
 * because both halves arrive as the same event type -- a content-block scan would
 * bucket every one of them the same way.
 */
export function zcodeSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  if (zcodeControlPlanText(parsed.parentObject) !== null)
    return 'request'
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.ToolUpdated)
    return 'other'
  return zcodeToolSpanRole(pickString(envelope.payload, 'kind'), parsed)
}

/**
 * Whether one row draws from its span's request.
 *
 * A `result`, `error` or `batch` frame states no tool name and no input, so its row
 * reads them from the paired `scheduled` frame. A retained `started` or `progress`
 * frame states no input either. A retained `scheduled` frame states its own, unless
 * ZCode omitted the input from the frame. A request row never needs anything beside
 * itself, and neither does a frame that states no call, such as a `batch` frame that
 * lists several calls: its row draws no call to complete.
 */
export function zcodeNeedsRequest(parsed: ResolvedMessageContent): boolean {
  if (zcodeSpanRole(parsed) !== 'result')
    return false
  const tool = zcodeExtractTool(parsed.parentObject)
  return tool !== null && (!tool.toolName || Object.keys(tool.input).length === 0)
}
