import type { ContentBlock } from '~/lib/contentBlocks'
import { asContentArray, getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'

/**
 * Extract `message.content` array from a Claude `{type: 'assistant',
 * message: {content: [...]}}` envelope, or null when the shape doesn't
 * match. Used by Claude's `extractQuotableText` and by the AskUserQuestion
 * tool-result renderer.
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
    ? joinContentParagraphs(asContentArray(inner), { text: 'text' })
    : String(inner || '')
  return text || null
}
