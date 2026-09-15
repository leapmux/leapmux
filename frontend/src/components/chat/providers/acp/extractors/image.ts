import type { ToolMessageInput } from '../../registry'
import type { ACPToolAdapter } from '../toolPresentation'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { TOOL_FILE_PATH_KEYS } from '~/components/chat/results/toolInputs'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickFirstString, pickObject } from '~/lib/jsonPick'
import { flattenAcpContent } from '../content'
import { acpToolPresentation, resolveACPToolCall } from '../toolPresentation'

/**
 * Extract every ACP image in wire order.
 * The shared parser handles file reads, fetched images, and screenshots.
 * Convert a file URI to its path. Use the request path when the image omits one.
 */
export function acpImagesFromToolCall(toolUse: Record<string, unknown> | null | undefined): ImageResultSource[] {
  if (!toolUse)
    return []
  const fallbackPath = pickFirstString(pickObject(toolUse, 'rawInput'), TOOL_FILE_PATH_KEYS)
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
  input: ToolMessageInput,
  adapter?: ACPToolAdapter,
): ImageResultSource[] {
  const parsed = input.parsed.parentObject
  if (!isObject(parsed))
    return []
  // The resolved ACP message contains the tool fields directly.
  const tool = resolveACPToolCall(parsed, input.request?.parentObject)
  const presentation = acpToolPresentation(tool, adapter, input.parsed.supplementalContent, input.parsed.completion)
  const body = presentation.body
  const primary = body.type === 'mcp'
    ? body.source.content.flatMap(item => item.type === 'image' ? [item.source] : [])
    : acpImagesFromToolCall({ ...tool, rawInput: presentation.input })
  const additional = presentation.additionalContent?.content.flatMap(item => item.type === 'image' ? [item.source] : []) ?? []
  return [...additional, ...primary]
}
