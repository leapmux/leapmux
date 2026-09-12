import type { McpContentItem } from '../../../results/mcpToolCall'
import type { ZCodeRow } from './toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { ZCODE_DISPLAY } from '../protocol'
import { zcodeNativeTool } from './toolCommon'

/** Restore the provider's content order without parsing its generated attachment labels. */
export function zcodeMcpContent(row: ZCodeRow): McpContentItem[] | null {
  const native = zcodeNativeTool(row)
  const layout = pickObject(native?.state, 'metadata')?.modelContentLayout
  const attachments = native?.state.attachments
  if (!native || !Array.isArray(layout) || layout.length === 0 || !Array.isArray(attachments) || attachments.length === 0)
    return null
  const content: McpContentItem[] = []
  for (const entry of layout) {
    if (!isObject(entry))
      return null
    if (entry.type === 'text' && typeof entry.text === 'string') {
      content.push({ type: 'text', text: entry.text })
      continue
    }
    const index = entry.attachmentIndex
    if (entry.type !== 'attachment' || typeof index !== 'number' || !Number.isSafeInteger(index) || index < 0 || index >= attachments.length)
      return null
    const attachment = attachments[index]
    if (!isObject(attachment) || attachment.type !== 'file'
      || attachment.sessionID !== native.sessionId || attachment.messageID !== native.messageId) {
      return null
    }
    const mimeType = pickString(attachment, 'mime').trim().toLowerCase()
    const uri = pickString(pickObject(attachment, 'metadata'), 'artifactUri') || pickString(attachment, 'url')
    const description = pickString(attachment, 'filename', undefined)
    if (mimeType.startsWith('image/')) {
      const url = pickString(native.artifacts, uri, undefined)
      content.push({ type: 'image', source: { mimeType, url, description: description === 'MCP image' ? undefined : description } })
    }
    else {
      content.push({ type: 'resource', uri, mimeType, text: description })
    }
  }
  return content
}

/** Native display hints can carry images without a separate artifact record. */
export function zcodeDisplayImages(display: Record<string, unknown> | null | undefined): ImageResultSource[] {
  if (!display)
    return []
  const nodeImages = display.kind === ZCODE_DISPLAY.NodeImages
  const media = nodeImages ? display.images : display.kind === ZCODE_DISPLAY.ComputerUse ? display.media : null
  if (!Array.isArray(media))
    return []
  return media.flatMap((entry): ImageResultSource[] => {
    if (!isObject(entry))
      return []
    const source = parseImageBlock({
      type: 'image',
      mimeType: pickString(entry, 'mimeType', undefined),
      data: pickString(entry, nodeImages ? 'base64' : 'data', undefined),
      url: pickString(entry, 'artifactUri', undefined),
    })
    return source ? [source] : []
  })
}
