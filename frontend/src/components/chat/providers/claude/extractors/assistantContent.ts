import type { ContentBlock } from '~/lib/contentBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { asContentArray, getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

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

/** Extract tool name and input from a parsed Claude tool_use message. */
export function extractToolUseInfo(parsed: ParsedMessageContent): { toolName: string, input: Record<string, unknown> } | null {
  const obj = parsed.parentObject
  if (!obj)
    return null
  const content = getAssistantContent(obj)
  if (!content)
    return null
  const toolUse = content.find(c => isObject(c) && c.type === 'tool_use')
  if (!toolUse)
    return null
  const toolData = toolUse as Record<string, unknown>
  return {
    toolName: pickString(toolData, 'name'),
    input: pickObject(toolData, 'input', {}),
  }
}

/**
 * The text body of ONE Claude tool_result block's `content`: a plain string, or the joined text of a
 * nested Anthropic-style block array (text blocks). Returns null when neither yields text. The single
 * home for the string-vs-array unwrap -- the fiddly part -- shared by {@link extractToolResultText}
 * (the FIRST tool_result) and {@link joinToolResultText} (EVERY tool_result), so the two can't drift.
 */
function toolResultBlockText(block: ContentBlock): string | null {
  const inner = block.content
  if (typeof inner === 'string')
    return inner || null
  const nested = asContentArray(inner)
  if (!nested)
    return null
  return joinContentParagraphs(nested, { text: 'text' }) || null
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
  return tr ? toolResultBlockText(tr) : null
}

/**
 * Join the textual body of EVERY tool_result block in a Claude
 * `{message:{content:[...]}}` envelope (distinct from {@link extractToolResultText},
 * which returns only the FIRST). Parallel tool calls join with a blank line. A
 * tool_result's `content` is either a plain string or a nested Anthropic-style block
 * array (text/image blocks); both are handled. Used by Claude's scroll-rail `previewText`
 * to preview a self-displaying control-response answer (AskUserQuestion / ExitPlanMode).
 * Returns null when there is no tool_result text.
 */
export function joinToolResultText(parsed: Record<string, unknown> | null | undefined): string | null {
  const content = getMessageContentArray(parsed)
  if (!content)
    return null
  const parts: string[] = []
  for (const block of content) {
    if (!isObject(block) || block.type !== 'tool_result')
      continue
    const text = toolResultBlockText(block)
    if (text)
      parts.push(text)
  }
  return parts.length > 0 ? parts.join('\n\n') : null
}
