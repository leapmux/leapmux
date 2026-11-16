import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type {} from '../registry'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { codewhaleToolFrame, codewhaleToolSpanRole } from './extractors/toolCommon'

/**
 * Codewhale span role. The frame decides: an item's start and a `tool_use` block open
 * a call, and an item's final event and a `tool_result` block end it. A retained row is
 * final whatever its frame.
 */
export function codewhaleSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const frame = codewhaleToolFrame(parsed.parentObject)
  return frame
    ? codewhaleToolSpanRole(frame, parsed)
    : 'other'
}

/**
 * Whether one row draws from its span's request.
 *
 * A subagent's result block states neither the tool nor its arguments, so its row
 * reads them from the `tool_use` block. A final item states the tool, and it repeats
 * the arguments as a JSON string, so its row needs the request only when that string
 * states none. A request row never needs anything beside itself, and neither does an
 * OPENING frame that a turn's end retained as the final row: its row reads its own
 * frame as the request (see codewhaleToolFacts), also for a tool that takes no argument.
 */
export function codewhaleNeedsRequest(parsed: ResolvedMessageContent): boolean {
  const frame = codewhaleToolFrame(parsed.parentObject)
  if (!frame || frame.outcome === 'open' || codewhaleToolSpanRole(frame, parsed) !== 'result')
    return false
  return !frame.toolName || Object.keys(frame.input).length === 0
}
