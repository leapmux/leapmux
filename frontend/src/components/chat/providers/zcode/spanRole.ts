import type { SpanRole } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeControlPlanText } from './extractors/plan'
import { zcodeEnvelope, zcodeToolSpanRole } from './extractors/toolCommon'

/**
 * ZCode span role. The `tool.updated` KIND discriminates the opener from the result,
 * because both halves arrive as the same event type -- a content-block scan would
 * bucket every one of them the same way.
 */
export function zcodeSpanRole(parsed: ParsedMessageContent): SpanRole {
  if (zcodeControlPlanText(parsed.parentObject) !== null)
    return 'opener'
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.ToolUpdated)
    return 'other'
  return zcodeToolSpanRole(pickString(envelope.payload, 'kind'), parsed)
}
