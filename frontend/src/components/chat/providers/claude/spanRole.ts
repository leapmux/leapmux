import type {} from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { getMessageContent } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'

/**
 * Claude/Anthropic span role: a `tool_use` content block marks a request, and a `tool_result` block
 * marks a result. Scan every block before deciding and let the `tool_use` request win. A message
 * that holds BOTH blocks is the request because it carries the tool input. Returning on the first
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
