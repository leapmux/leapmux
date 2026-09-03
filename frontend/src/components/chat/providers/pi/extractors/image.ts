import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { asContentArray, splitToolResultContent } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Every image a Pi tool result carries, in wire order.
 *
 * Pi's tool results are first-class multimodal: `result.content` is a
 * `(TextContent | ImageContent)[]` where `ImageContent` is
 * `{type:'image', data, mimeType}` -- the MCP shape. `read` on an image file
 * produces one, as do its MCP bridges and screenshot tools.
 *
 * The path comes from the paired `tool_execution_start`, because the `end`
 * event carries no args. Absent that pairing the image still renders; only the
 * click target is lost.
 */
export function piToolResultImages(
  parsed: unknown,
  _spanType: string | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
): ImageResultSource[] {
  if (!isObject(parsed))
    return []
  const blocks = asContentArray(pickObject(parsed, 'result')?.content)
  if (!blocks)
    return []
  const images = splitToolResultContent(blocks, { text: 'text' }).images
  const filePath = pickString(pickObject(toolUseParsed?.parentObject, 'args'), 'filePath', undefined)
  if (!filePath)
    return images
  return images.map(source => source.filePath ? source : { ...source, filePath })
}
