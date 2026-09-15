import type { CopilotToolRow } from './toolPresentation'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { TOOL_FILE_PATH_KEYS } from '~/components/chat/results/toolInputs'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { pickFirstString } from '~/lib/jsonPick'

/**
 * Every image one tool result carries, in wire order.
 *
 * Copilot returns rich content as `result.contents`, in the Model Context Protocol
 * block shape the shared parser already reads. A block that states no path takes the
 * request's own path, so a screenshot of a file keeps the file it came from.
 */
export function copilotToolImages(row: CopilotToolRow): ImageResultSource[] {
  const contents = Array.isArray(row.raw?.contents) ? row.raw.contents : []
  const fallbackPath = pickFirstString(row.input, TOOL_FILE_PATH_KEYS)
  const images: ImageResultSource[] = []
  for (const block of contents) {
    const source = parseImageBlock(block)
    if (source)
      images.push(withFallbackFilePath(source, fallbackPath))
  }
  return images
}
