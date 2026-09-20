import type { ContentBlock } from '~/lib/contentBlocks'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { pickFirstString, pickObject } from '~/lib/jsonPick'
import { TOOL_FILE_PATH_KEYS } from '../../toolInputKeys'
import { flattenAcpContent } from '../content'

/**
 * Extract every ACP image in wire order.
 * The shared parser handles file reads, fetched images, and screenshots.
 * Convert a file URI to its path. Use the request path when the image omits one.
 */
export function acpImagesFromToolCall(toolUse: Record<string, unknown> | null | undefined): ImageResultSource[] {
  if (!toolUse)
    return []
  return acpImagesFromContent(flattenAcpContent(toolUse.content), pickObject(toolUse, ACP_SUPPLEMENT_REQUEST.RawInput))
}

/** Extract images from content that the caller normalized once. */
export function acpImagesFromContent(content: ContentBlock[], rawInput: Record<string, unknown> | null | undefined): ImageResultSource[] {
  const fallbackPath = pickFirstString(rawInput, TOOL_FILE_PATH_KEYS)
  const images: ImageResultSource[] = []
  for (const block of content) {
    const source = parseImageBlock(block)
    if (!source)
      continue
    images.push(withFallbackFilePath(source, fallbackPath))
  }
  return images
}
