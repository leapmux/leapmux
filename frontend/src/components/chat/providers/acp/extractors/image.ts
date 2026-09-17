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
  const fallbackPath = pickFirstString(pickObject(toolUse, ACP_SUPPLEMENT_REQUEST.RawInput), TOOL_FILE_PATH_KEYS)
  const images: ImageResultSource[] = []
  for (const block of flattenAcpContent(toolUse.content)) {
    const source = parseImageBlock(block)
    if (!source)
      continue
    images.push(withFallbackFilePath(source, fallbackPath))
  }
  return images
}
