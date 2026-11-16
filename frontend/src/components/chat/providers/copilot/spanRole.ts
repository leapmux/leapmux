import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { retainedRowIsFinal } from '../registry'
import { copilotEvent, copilotEventData } from './protocol'

/**
 * The `tool.execution_start` row opens a span; its completion closes it.
 *
 * A turn that ended while the call ran stores the start frame AGAIN as the closing
 * row, because the runtime sends no completion for it. The completion column is what
 * separates the two copies.
 */
export function copilotSpanRole(parsed: ResolvedMessageContent) {
  switch (copilotEvent(parsed.parentObject)?.type) {
    case COPILOT_EVENT.ToolStarted:
      return retainedRowIsFinal(parsed.completion)
        ? 'result' as const
        : 'request' as const
    case COPILOT_EVENT.ToolCompleted:
      return 'result' as const
    default:
      return 'other' as const
  }
}

/**
 * Whether one row draws from its span's request: a `tool.execution_complete` event
 * states neither the tool's name nor its arguments, so it reads them off the start. A
 * start row, also one that a stopped turn retained, reads its own event.
 */
export function copilotNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return copilotEventData(parsed.parentObject, COPILOT_EVENT.ToolCompleted) !== null
}
