import { isObject, pickObject } from '~/lib/jsonPick'

/**
 * Extract `message.content` array from a Claude `{type: 'assistant',
 * message: {content: [...]}}` envelope, or null when the shape doesn't
 * match. Used by Claude's `extractQuotableText` and by the AskUserQuestion
 * tool-result renderer.
 */
export function getAssistantContent(parsed: unknown): Array<Record<string, unknown>> | null {
  if (!isObject(parsed) || parsed.type !== 'assistant')
    return null
  return getMessageContentArray(parsed)
}

/**
 * Read `message.content` as an array of content blocks regardless of
 * envelope `type` (Claude `{type: 'assistant'|'user', message: {content:
 * [...]}}`). Returns null when the inner shape isn't a content array.
 */
export function getMessageContentArray(parsed: unknown): Array<Record<string, unknown>> | null {
  if (!isObject(parsed))
    return null
  const message = pickObject(parsed, 'message')
  if (!message)
    return null
  const content = message.content
  if (!Array.isArray(content))
    return null
  return content as Array<Record<string, unknown>>
}

/**
 * Concatenate all `{type: 'text'|'thinking', ...}` text out of a Claude
 * content-block array. Returns the empty string when no text blocks are
 * present.
 */
export function joinContentText(content: Array<Record<string, unknown>>, blockType: 'text' | 'thinking' = 'text'): string {
  const field = blockType === 'thinking' ? 'thinking' : 'text'
  let result = ''
  for (const c of content) {
    if (isObject(c) && c.type === blockType) {
      const v = c[field]
      if (typeof v === 'string')
        result += v
    }
  }
  return result
}

/**
 * Pull the text content out of a Claude `tool_result` block inside a
 * parsed `{message:{content:[...]}}` envelope. Returns null when the
 * envelope carries no tool_result or its content has no text blocks.
 */
export function extractToolResultText(parsed: Record<string, unknown> | null | undefined): string | null {
  const content = getMessageContentArray(parsed)
  if (!content)
    return null
  const tr = content.find(c => isObject(c) && c.type === 'tool_result')
  if (!tr)
    return null
  const inner = tr.content
  const text = Array.isArray(inner)
    ? joinContentText(inner as Array<Record<string, unknown>>)
    : String(inner || '')
  return text || null
}
