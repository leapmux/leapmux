import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { grokRawOutput } from './results'

/** Grok replaces an extracted image's data URI with this text in its MCP output. */
const GROK_MCP_IMAGE_PLACEHOLDER = '[image content will be provided separately]'

/** Grok can also leave an image as a data URI in the output text. */
const GROK_MCP_IMAGE_IN_TEXT = /\[image content will be provided separately\]|data:image\/[a-z\d.+-]+;base64,[a-z\d+/]+={0,2}/gi

function extractedImages(raw: Record<string, unknown>): Array<McpContentItem | null> {
  const entries = raw.extracted_images
  if (!Array.isArray(entries))
    return []
  return entries.map((entry) => {
    if (!isObject(entry))
      return null
    const data = pickString(entry, 'data')
    const mimeType = pickString(entry, 'mime_type')
    if (!data || !mimeType.startsWith('image/'))
      return null
    return { type: 'image', source: { url: `data:${mimeType};base64,${data}` } }
  })
}

/** Read Grok's MCP result from the ACP content or its native raw output. */
export function grokMcpContent(facts: ACPToolFacts): McpContentItem[] {
  const content = facts.content.map(parseMcpContentItem)
  const raw = grokRawOutput(facts.tool, 'MCP')
  if (!raw)
    return content
  const images = extractedImages(raw)
  const output = pickString(pickObject(raw, 'output'), 'OkayOutput', undefined)
  if (content.length > 0) {
    const seen = new Set(content.filter(item => item.type === 'image').map(item => item.source.url))
    return [...content, ...images.filter((item): item is McpContentItem & { type: 'image' } => item?.type === 'image' && !seen.has(item.source.url))]
  }
  if (output === undefined)
    return images.filter((item): item is McpContentItem => item !== null)

  const result: McpContentItem[] = []
  let start = 0
  let imageIndex = 0
  let sawMarker = false
  for (const match of output.matchAll(GROK_MCP_IMAGE_IN_TEXT)) {
    sawMarker = true
    const position = match.index
    const before = output.slice(start, position).trim()
    if (before)
      result.push({ type: 'text', text: before })
    if (match[0] === GROK_MCP_IMAGE_PLACEHOLDER) {
      const image = images[imageIndex++]
      result.push(image ?? { type: 'text', text: match[0] })
    }
    else {
      result.push({ type: 'image', source: { url: match[0] } })
    }
    start = position + match[0].length
  }
  if (!sawMarker) {
    if (output)
      result.push({ type: 'text', text: output })
    result.push(...images.filter((item): item is McpContentItem => item !== null))
    return result
  }
  const after = output.slice(start).trim()
  if (after)
    result.push({ type: 'text', text: after })
  const seen = new Set(result.filter(item => item.type === 'image').map(item => item.source.url))
  result.push(...images.slice(imageIndex).filter((item): item is McpContentItem & { type: 'image' } => item?.type === 'image' && !seen.has(item.source.url)))
  return result
}
