import type { ParsedMessageContent } from '~/lib/messageParser'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { retainedRowIsFinal } from '../registry'
import { copilotToolRow } from './extractors/toolCall'
import { copilotEvent } from './protocol'

/**
 * The `tool.execution_start` row opens a span; its completion closes it.
 *
 * A turn that ended while the call ran stores the start frame AGAIN as the closing
 * row, because the runtime sends no completion for it. The completion column is what
 * separates the two copies.
 */
export function copilotSpanRole(parsed: ParsedMessageContent) {
  switch (copilotEvent(parsed.parentObject)?.type) {
    case COPILOT_EVENT.ToolStarted:
      return retainedRowIsFinal(parsed.completion) ? 'result' as const : 'request' as const
    case COPILOT_EVENT.ToolCompleted:
      return 'result' as const
    default:
      return 'other' as const
  }
}

export function copilotRelatedMessages(parsed: ParsedMessageContent) {
  const role = copilotSpanRole(parsed)
  if (role === 'result')
    return ['request'] as const
  if (role !== 'request')
    return []
  const row = copilotToolRow(parsed.parentObject)
  return row && (row.kind === 'agent' || Object.keys(row.input).length === 0) ? ['result'] as const : []
}
