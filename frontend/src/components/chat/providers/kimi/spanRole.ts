import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'
import { retainedRowIsFinal } from '../registry'
import { kimiEvent } from './protocol'

/**
 * `tool.call.started` opens a span and `tool.result` closes it.
 *
 * A turn that ended while the call ran stores the start frame AGAIN as the closing
 * row. The completion column is what separates the two copies.
 */
export function kimiSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  switch (kimiEvent(parsed.parentObject)?.type) {
    case KIMI_EVENT.ToolCallStarted:
      return retainedRowIsFinal(parsed.completion)
        ? 'result'
        : 'request'
    case KIMI_EVENT.ToolResult:
      return 'result'
    default:
      return 'other'
  }
}

/**
 * Whether one row draws from its span's request: a `tool.result` states no tool name,
 * no arguments and no display, so it reads them from the paired `tool.call.started`. A
 * start frame, also one that a stopped turn retained, states its own.
 */
export function kimiNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return kimiEvent(parsed.parentObject)?.type === KIMI_EVENT.ToolResult
}
