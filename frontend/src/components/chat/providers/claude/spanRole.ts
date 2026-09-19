import type {} from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { getMessageContent } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'

/**
 * Claude/Anthropic span role: a `tool_use` content block marks an opener, a `tool_result` block a
 * result. Scan every block before deciding and let the `tool_use` opener win -- a message holding
 * BOTH blocks IS the opener (it carries the tool input to render); early-returning on the first
 * tool_result would mis-bucket it as a result and drop its input.
 */
export function claudeSpanRole(parsed: ParsedMessageContent): ToolSpanRole {
  const blocks = getMessageContent(parsed.parentObject ?? undefined)
  if (!blocks)
    return 'other'
  let hasToolUse = false
  let hasToolResult = false
  for (const b of blocks) {
    if (!isObject(b))
      continue
    if (b.type === 'tool_use')
      hasToolUse = true
    else if (b.type === 'tool_result')
      hasToolResult = true
  }
  return hasToolUse ? 'request' : hasToolResult ? 'result' : 'other'
}
