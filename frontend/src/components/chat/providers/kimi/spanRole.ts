import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
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
      return retainedRowIsFinal(parsed.completion) ? 'result' : 'request'
    case KIMI_EVENT.ToolResult:
      return 'result'
    default:
      return 'other'
  }
}

/**
 * The rows one tool row needs beside it.
 *
 * A result states no tool name and no arguments, so it needs its request. A subagent
 * launch needs its result, which carries the run the card reports, and so does a call
 * whose arguments state nothing to show.
 */
export function kimiRelatedMessages(parsed: ResolvedMessageContent) {
  const role = kimiSpanRole(parsed)
  if (role === 'result')
    return ['request'] as const
  if (role !== 'request')
    return []
  const start = kimiEvent(parsed.parentObject)?.data
  const name = pickString(start, 'name')
  const args = pickObject(start, 'args') ?? {}
  return name === KIMI_TOOL.Agent || name === KIMI_TOOL.AgentSwarm || Object.keys(args).length === 0 ? ['result'] as const : []
}
