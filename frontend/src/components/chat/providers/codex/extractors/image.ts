import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_ITEM } from '~/types/toolMessages'

/** The one format Codex's image generation returns; it hardcodes the same. */
const CODEX_GENERATED_IMAGE_MIME = 'image/png'

/** The generated image an `imageGeneration` item carries, if it produced one. */
export function codexGeneratedImage(item: Record<string, unknown> | null | undefined): ImageResultSource | null {
  if (!item || item.type !== CODEX_ITEM.IMAGE_GENERATION)
    return null
  // `result` is empty while the item is in progress and after a failure.
  const data = pickString(item, 'result', undefined)?.trim()
  if (!data)
    return null
  const savedPath = pickString(item, 'savedPath', undefined)
  return savedPath
    ? { data, mimeType: CODEX_GENERATED_IMAGE_MIME, filePath: savedPath }
    : { data, mimeType: CODEX_GENERATED_IMAGE_MIME }
}

/**
 * Every image a Codex item carries, in wire order.
 *
 * Three item types can hold one:
 *   - `mcpToolCall` -- `result.content[]` in the MCP shape.
 *   - `dynamicToolCall` -- `contentItems[]` with `{type:'inputImage', imageUrl}`.
 *   - `imageGeneration` -- a base64 PNG in `result`.
 *
 * `imageView` is deliberately absent: it states the path of a file the agent
 * looked at and carries no pixels, so there is nothing to render or open from
 * the message. Its renderer links the path instead.
 */
export function codexToolResultImages(
  parsed: unknown,
  _spanType: string | undefined,
  _toolUseParsed: ParsedMessageContent | undefined,
): ImageResultSource[] {
  if (!isObject(parsed))
    return []
  const item = pickObject(parsed, 'item')
  if (!item)
    return []

  const generated = codexGeneratedImage(item)
  if (generated)
    return [generated]

  const blocks = codexImageBearingBlocks(item)
  const images: ImageResultSource[] = []
  for (const block of blocks) {
    const source = parseImageBlock(block)
    if (source)
      images.push(source)
  }
  return images
}

/** The array a Codex item keeps its content blocks in, by item type. */
function codexImageBearingBlocks(item: Record<string, unknown>): Record<string, unknown>[] {
  const raw = item.type === CODEX_ITEM.MCP_TOOL_CALL
    ? pickObject(item, 'result')?.content
    : item.type === CODEX_ITEM.DYNAMIC_TOOL_CALL
      ? item.contentItems
      : null
  return Array.isArray(raw) ? raw.filter(isObject) : []
}
