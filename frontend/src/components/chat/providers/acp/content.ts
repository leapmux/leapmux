import type { ContentBlock } from '~/lib/contentBlocks'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Extract the nested metadata that command, search, and fetch results share. */
export function pickAcpRawOutputMetadata(toolUse: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
  return pickObject(pickObject(toolUse, ACP_SUPPLEMENT.RawOutput), 'metadata')
}

/**
 * Unwrap ACP content blocks for the shared content helpers.
 * Preserve every inner block type, including images from file reads and screenshots.
 * Leave top-level diff and terminal entries for their specific extractors.
 */
export function flattenAcpContent(content: unknown): ContentBlock[] {
  if (!Array.isArray(content))
    return []
  return content.flatMap((entry): ContentBlock[] => {
    if (!isObject(entry))
      return []
    if (entry.type === 'content' && isObject(entry.content)) {
      const inner = entry.content
      // Some providers omit the type from text blocks. Preserve their command output.
      const text = pickString(inner, 'text')
      return text && (inner.type === undefined || inner.type === 'text') ? [{ type: 'text', text }] : [inner]
    }
    return [entry]
  })
}

/**
 * Join text from the content array, or read the raw output when no text exists.
 * Combine the raw output and error fields when either field exists.
 * Exclude images because the image renderer handles them separately.
 */
export function collectAcpToolText(toolUse: Record<string, unknown> | null | undefined, options: { rawObjects?: boolean } = {}): string {
  if (!toolUse)
    return ''
  const text = joinContentParagraphs(flattenAcpContent(toolUse.content), { text: 'text' }, () => null)
  if (text)
    return text
  const raw = toolUse.rawOutput
  const format = (value: unknown) => value === undefined || value === null
    ? ''
    : typeof value === 'object' ? prettifyJson(value) : String(value)
  if (!isObject(raw))
    return format(raw)
  if ('output' in raw || 'error' in raw)
    return [format(raw.output), format(raw.error)].filter(value => value !== '').join('\n')
  return options.rawObjects === false ? '' : format(raw)
}
