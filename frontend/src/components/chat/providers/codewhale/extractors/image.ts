import type { McpContentItem } from '../../../model/mcpToolCall'
import type { CodewhaleToolFrame } from './toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { CODEWHALE_ITEM_METADATA, CODEWHALE_MEDIA_FIELD, CODEWHALE_MEDIA_RULE, CODEWHALE_MEDIA_TYPE } from '~/generated/contracts/codewhale-protocol'
import { parseDataImageUrl } from '~/lib/imageBlocks'
import { isObject } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { codewhaleToolOutputFiles } from '../toolSupplement'

const IMAGE_MIME_TYPES: ReadonlySet<string> = new Set([CODEWHALE_MEDIA_TYPE.PNG, CODEWHALE_MEDIA_TYPE.JPEG, CODEWHALE_MEDIA_TYPE.GIF, CODEWHALE_MEDIA_TYPE.WebP])
const SESSION_ID = /^[\w-]+$/
const DIGEST = /^[a-f0-9]{64}$/
const OUTPUT_FILE_ID = /^art_image_[a-f0-9]{64}$/

function positiveInteger(value: unknown, maximum: number): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 && value <= maximum
}

/** Check canonical base64 size before supplying a recovered data URI to the image renderer. */
function encodedSize(base64: string): number | null {
  if (base64.length === 0 || base64.length % 4 !== 0)
    return null
  const padding = base64.endsWith('==') ? 2 : base64.endsWith('=') ? 1 : 0
  for (let index = 0; index < base64.length - padding; index++) {
    const char = base64.charCodeAt(index)
    if (!((char >= 65 && char <= 90) || (char >= 97 && char <= 122) || (char >= 48 && char <= 57) || char === 43 || char === 47))
      return null
  }
  const last = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'.indexOf(base64[base64.length - padding - 1] ?? '=')
  if ((padding === 2 && last % 16 !== 0) || (padding === 1 && last % 4 !== 0))
    return null
  return base64.length / 4 * 3 - padding
}

/** Read one native image descriptor and its separately persisted artifact body. */
export function codewhaleToolMediaImages(frame: CodewhaleToolFrame | null, supplemental: unknown): ImageResultSource[] {
  if (!frame || frame.outcome !== 'completed')
    return []
  const media = frame.metadata[CODEWHALE_ITEM_METADATA.ToolMedia]
  if (!Array.isArray(media) || media.length !== 1 || !isObject(media[0]))
    return []
  const descriptor = media[0]
  const session = descriptor[CODEWHALE_MEDIA_FIELD.SessionID]
  const outputFile = descriptor[CODEWHALE_MEDIA_FIELD.OutputFileID]
  const mime = descriptor[CODEWHALE_MEDIA_FIELD.MediaType]
  const size = descriptor[CODEWHALE_MEDIA_FIELD.ByteSize]
  const hash = descriptor[CODEWHALE_MEDIA_FIELD.SHA256]
  const width = descriptor[CODEWHALE_MEDIA_FIELD.Width]
  const height = descriptor[CODEWHALE_MEDIA_FIELD.Height]
  if (descriptor[CODEWHALE_MEDIA_FIELD.Version] !== CODEWHALE_MEDIA_RULE.CurrentVersion
    || descriptor[CODEWHALE_MEDIA_FIELD.ToolCallID] !== frame.callId
    || typeof session !== 'string' || !SESSION_ID.test(session)
    || typeof outputFile !== 'string' || !OUTPUT_FILE_ID.test(outputFile)
    || typeof mime !== 'string' || !IMAGE_MIME_TYPES.has(mime)
    || typeof hash !== 'string' || !DIGEST.test(hash)
    || !positiveInteger(size, CODEWHALE_MEDIA_RULE.MaxImageBytes)
    || !positiveInteger(width, CODEWHALE_MEDIA_RULE.MaxDimension)
    || !positiveInteger(height, CODEWHALE_MEDIA_RULE.MaxDimension)
    || width * height > CODEWHALE_MEDIA_RULE.MaxPixels) {
    return []
  }
  const source: ImageResultSource = { mimeType: mime, dimensions: { width, height } }
  const body = codewhaleToolOutputFiles(supplemental)?.[outputFile]
  if (typeof body === 'string' && body.length <= Math.ceil(CODEWHALE_MEDIA_RULE.MaxImageBytes / 3) * 4 + 64) {
    const parsed = parseDataImageUrl(body)
    if (parsed?.mimeType === mime && encodedSize(parsed.base64) === size)
      source.url = body
  }
  return [source]
}

/** Keep native MCP block order and replace only the recovered image placeholder. */
export function codewhaleToolContent(text: string, images: readonly ImageResultSource[]): McpContentItem[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  }
  catch {
    parsed = null
  }
  if (!isObject(parsed) || !Array.isArray(parsed.content))
    return [...(text ? [{ type: 'text' as const, text }] : []), ...images.map(source => ({ type: 'image' as const, source }))]
  let imageIndex = 0
  const content = parsed.content.map((raw: unknown): McpContentItem => {
    const item = parseMcpContentItem(raw)
    if (item.type !== 'image')
      return item
    const source = images[imageIndex++]
    // The runtime removes native image payloads from this text copy.
    if (source)
      return { type: 'image', source }
    const nativeData = item.source.data
    const nativeSize = nativeData ? encodedSize(nativeData) : null
    if (nativeSize !== null && nativeSize > 0 && nativeSize <= CODEWHALE_MEDIA_RULE.MaxImageBytes && item.source.mimeType && IMAGE_MIME_TYPES.has(item.source.mimeType))
      return item
    return { type: 'image', source: item.source.mimeType === undefined ? {} : { mimeType: item.source.mimeType } }
  })
  const extra = Object.fromEntries(Object.entries(parsed).filter(([key]) => key !== 'content'))
  if (Object.keys(extra).length > 0)
    content.push({ type: 'unknown', raw: extra })
  for (const source of images.slice(imageIndex))
    content.push({ type: 'image', source })
  return content
}
