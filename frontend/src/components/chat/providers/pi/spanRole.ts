import type {} from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'

/**
 * Pi span role: the flat envelope `type` discriminates the request from the result.
 * Pi's `tool_execution_end` carries no Anthropic content blocks, so the default content-block scan
 * would mis-bucket it as `other` -- routing by `type` files it as a result regardless of arrival
 * order.
 */
export function piSpanRole(parsed: ParsedMessageContent): ToolSpanRole {
  const type = pickString(parsed.parentObject, 'type')
  if (type === PI_EVENT.ToolExecutionEnd)
    return 'result'
  if (type !== PI_EVENT.ToolExecutionStart)
    return 'other'
  // A turn that ended while the call ran stores the start frame AGAIN as the closing
  // row, so the completion is what separates the two copies.
  return retainedRowIsFinal(parsed.completion) ? 'result' : 'request'
}
