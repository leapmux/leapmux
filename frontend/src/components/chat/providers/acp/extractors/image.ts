import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickFirstString, pickObject } from '~/lib/jsonPick'
import { ACP_FILE_PATH_KEYS, flattenAcpContent } from '../rendering'

/**
 * Every image an ACP tool call carries, in wire order.
 *
 * ACP wraps each block as `{type:'content', content: ContentBlock}`, and its
 * `ImageContent` is `{type:'image', data, mimeType, uri?}` -- the MCP shape.
 * OpenCode and Kilo send one for a `read` on an image, a `webfetch` that
 * returned an image, and every MCP image; Goose sends one for `image_read` and
 * for its computer-control screenshots.
 *
 * `uri` is a `file://` URL when the agent read the image off disk, and
 * `parseImageBlock` turns it into `filePath`. When the agent omits it, the
 * tool's own `rawInput` path is the next best answer -- it is the file the
 * user asked for.
 */
export function acpImagesFromToolCall(toolUse: Record<string, unknown> | null | undefined): ImageResultSource[] {
  if (!toolUse)
    return []
  const fallbackPath = pickFirstString(pickObject(toolUse, 'rawInput'), ACP_FILE_PATH_KEYS)
  const images: ImageResultSource[] = []
  for (const block of flattenAcpContent(toolUse.content)) {
    const source = parseImageBlock(block)
    if (!source)
      continue
    images.push(withFallbackFilePath(source, fallbackPath))
  }
  return images
}

/** `Provider.toolResultImages` for every ACP-based provider. */
export function acpToolResultImages(
  parsed: unknown,
  _spanType: string | undefined,
  _toolUseParsed: ParsedMessageContent | undefined,
): ImageResultSource[] {
  if (!isObject(parsed))
    return []
  // An ACP row is the `session/update` params: the tool call IS the message.
  return acpImagesFromToolCall(parsed)
}
