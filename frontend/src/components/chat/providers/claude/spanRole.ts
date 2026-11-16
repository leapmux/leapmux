import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { getMessageContent } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'

/** Which of the two span blocks a Claude envelope holds. */
function claudeSpanBlocks(parsed: ResolvedMessageContent): { toolUse: boolean, toolResult: boolean } {
  const blocks = getMessageContent(parsed.parentObject ?? undefined)
  let toolUse = false
  let toolResult = false
  for (const b of blocks ?? []) {
    if (!isObject(b))
      continue
    if (b.type === 'tool_use')
      toolUse = true
    else if (b.type === 'tool_result')
      toolResult = true
  }
  return { toolUse, toolResult }
}

/**
 * Claude/Anthropic span role: a `tool_use` content block marks a request, and a `tool_result` block
 * marks a result. Scan every block before deciding and let the `tool_use` request win. A message
 * that holds BOTH blocks is the request because it carries the tool input. Returning on the first
 * tool_result would mis-bucket it as a result and drop its input.
 *
 * A turn that ended while the call ran stores the `tool_use` frame AGAIN as the closing row, and
 * the completion column is what separates the two copies: that copy is the span's result.
 */
export function claudeSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const { toolUse, toolResult } = claudeSpanBlocks(parsed)
  if (toolUse) {
    return retainedRowIsFinal(parsed.completion)
      ? 'result'
      : 'request'
  }
  return toolResult
    ? 'result'
    : 'other'
}

/**
 * Whether a Claude row draws from its span's request: a `tool_result` block states
 * neither the tool name nor the arguments, so a result row that carries one needs its
 * request. A row with a `tool_use` block -- the request, and the closing copy of it that
 * a turn's end leaves -- carries both itself.
 */
export function claudeNeedsRequest(parsed: ResolvedMessageContent): boolean {
  const { toolUse, toolResult } = claudeSpanBlocks(parsed)
  return toolResult && !toolUse
}
