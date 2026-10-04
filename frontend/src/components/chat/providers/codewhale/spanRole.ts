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
  return frame ? codewhaleToolSpanRole(frame, parsed) : 'other'
}

/**
 * The linked rows one tool row needs.
 *
 * A result row needs its request, because a subagent's result block states neither
 * the tool nor its arguments. A request row needs its result for two reasons: the row
 * draws the whole call, and the FIRST call of a deferred tool is one the runtime never
 * ran. Only its result says so: the classifier hides that result row, and the request
 * row states the call with the runtime's own words.
 */
export function codewhaleRelatedMessages(parsed: ResolvedMessageContent) {
  const role = codewhaleSpanRole(parsed)
  if (role === 'result')
    return ['request'] as const
  return role === 'request' ? ['result'] as const : []
}
