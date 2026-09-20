import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type {} from '../registry'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { getMessageContent } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'
import { extractToolUseInfo } from './extractors/assistantContent'
import { canonicalClaudeToolName } from './toolKinds'
import { CLAUDE_TOOL_NAMES } from './toolNames'

const REQUEST_NEEDS_RESULT: ReadonlySet<string> = new Set([
  CLAUDE_TOOL_NAMES.AGENT,
  CLAUDE_TOOL_NAMES.TODO_WRITE,
  CLAUDE_TOOL_NAMES.TASK_CREATE,
  CLAUDE_TOOL_NAMES.TASK_UPDATE,
  CLAUDE_TOOL_NAMES.TASK_GET,
])

/**
 * Claude/Anthropic span role: a `tool_use` content block marks a request, and a `tool_result` block
 * marks a result. Scan every block before deciding and let the `tool_use` request win. A message
 * that holds BOTH blocks is the request because it carries the tool input. Returning on the first
 * tool_result would mis-bucket it as a result and drop its input.
 */
export function claudeSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
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

export function claudeRelatedMessages(parsed: ResolvedMessageContent) {
  if (claudeSpanRole(parsed) === 'result')
    return ['request'] as const
  const tool = canonicalClaudeToolName(extractToolUseInfo(parsed)?.toolName ?? '')
  return REQUEST_NEEDS_RESULT.has(tool) ? ['result'] as const : []
}
