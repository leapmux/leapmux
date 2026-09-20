import type { McpContentItem } from '../model/mcpToolCall'
import type { ChatRow } from '../model/row'
import type { ToolCall } from '../model/toolCall'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { rowDrawsResult } from '../model/derivations'
import { isGenericCall, typedResult } from '../model/toolCall'

function contentImages(content: readonly McpContentItem[]): ImageResultSource[] {
  return content.flatMap(item => item.type === 'image' ? [item.source] : [])
}

export function extraImages(call: ToolCall): ImageResultSource[] {
  return contentImages(call.extraContent ?? [])
}

export function resultImages(call: ToolCall): ImageResultSource[] {
  const result = isGenericCall(call) ? typedResult(call) : undefined
  return result ? contentImages(result.content) : []
}

export function imagesForRow(row: ChatRow | null | undefined): ImageResultSource[] {
  if (row?.kind !== 'tool' || !rowDrawsResult(row))
    return []
  return [...resultImages(row.call), ...extraImages(row.call), ...row.call.images]
}
