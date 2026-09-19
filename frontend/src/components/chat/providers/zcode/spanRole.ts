import type {} from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeControlPlanText } from './extractors/plan'
import { zcodeEnvelope, zcodeToolSpanRole } from './extractors/toolCommon'

/**
 * ZCode span role. The `tool.updated` KIND discriminates the request from the result,
 * because both halves arrive as the same event type -- a content-block scan would
 * bucket every one of them the same way.
 */
export function zcodeSpanRole(parsed: ParsedMessageContent): ToolSpanRole {
  if (zcodeControlPlanText(parsed.parentObject) !== null)
    return 'request'
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.ToolUpdated)
    return 'other'
  return zcodeToolSpanRole(pickString(envelope.payload, 'kind'), parsed)
}
