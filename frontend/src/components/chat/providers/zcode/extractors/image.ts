import type { ZCodeRow } from './toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { zcodeResultDisplay } from './display'
import { zcodeExtractTool } from './toolCommon'
import { zcodeDisplayImages } from './toolContent'

/** Extract the same image sources for the inline renderer and the image viewer. */
export function zcodeToolResultImages(row: ZCodeRow): ImageResultSource[] {
  const display = zcodeResultDisplay(row)
  return display?.kind === 'mcp'
    ? display.source.content.flatMap(item => item.type === 'image' ? [item.source] : [])
    : zcodeDisplayImages(zcodeExtractTool(row.parsed)?.result?.display)
}
