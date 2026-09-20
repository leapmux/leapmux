import type { ContentBlock } from '~/lib/contentBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { getMessageContent } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Extract `message.content` array from a Claude `{type: 'assistant',
 * message: {content: [...]}}` envelope, or null when the shape doesn't
 * match. Used internally by `extractToolUseInfo` in this file.
 */
export function getAssistantContent(parsed: unknown): ContentBlock[] | null {
  if (!isObject(parsed) || parsed.type !== 'assistant')
    return null
  return getMessageContentArray(parsed)
}

/**
 * Read `message.content` as a content-block array regardless of envelope
 * `type` (Claude `{type: 'assistant'|'user', message: {content: [...]}}`).
 * Returns null when the inner shape isn't a content array.
 */
export function getMessageContentArray(parsed: unknown): ContentBlock[] | null {
  return isObject(parsed) ? getMessageContent(parsed) : null
}

/** Extract tool name and input from a parsed Claude tool_use message. */
export function extractToolUseInfo(parsed: ParsedMessageContent, toolUseId?: string): { toolName: string, input: Record<string, unknown> } | null {
  const obj = parsed.parentObject
  if (!obj)
    return null
  const content = getAssistantContent(obj)
  if (!content)
    return null
  const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && (toolUseId === undefined || c.id === toolUseId))
  if (!toolUse)
    return null
  return {
    toolName: pickString(toolUse, 'name'),
    input: pickObject(toolUse, 'input', {}),
  }
}

/** Resolve input only from the request that matches the result's tool-use ID. */
export function extractPairedToolUseInfo(parsed: unknown, request?: ParsedMessageContent): ReturnType<typeof extractToolUseInfo> {
  const result = getMessageContentArray(parsed)?.find(block => isObject(block) && block.type === 'tool_result')
  const id = pickString(result, 'tool_use_id')
  return id && request ? extractToolUseInfo(request, id) : null
}
