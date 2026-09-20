import type { McpCallFacts, McpContentItem } from '../../../model/mcpToolCall'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { parseImageBlock } from '~/lib/imageBlocks'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../model/mcpToolCall'

/**
 * Read the wire facts of a Codex MCP item. Handles both `mcpToolCall`
 * (server-bound MCP tools) and `dynamicToolCall` (function-style dynamic
 * tools). Returns null otherwise.
 *
 * Wire shapes (codex-rs/app-server-protocol/.../v2/ThreadItem.ts):
 * - mcpToolCall: { server, tool, status, arguments, result?, error?, durationMs? }
 *   where result = { content: JsonValue[], structuredContent, _meta }
 * - dynamicToolCall: { namespace?, tool, status, arguments, contentItems?, success?, durationMs? }
 *   where contentItems is { type: 'inputText'|'inputImage', ... }[]
 */
export function codexMcpFromItem(item: Record<string, unknown> | null | undefined): McpCallFacts | null {
  if (!item)
    return null
  if (item.type === CODEX_ITEM.McpToolCall)
    return fromMcpToolCall(item)
  if (item.type === CODEX_ITEM.DynamicToolCall)
    return fromDynamicToolCall(item)
  return null
}

function fromMcpToolCall(item: Record<string, unknown>): McpCallFacts {
  const failed = item.status === 'failed' || item.status === 'cancelled' || item.status === 'declined' || undefined
  const argsJson = prettifyArgsJson(item.arguments)

  const result = pickObject(item, 'result')
  const rawContent: unknown[] = result && Array.isArray(result.content) ? result.content : []
  const content: McpContentItem[] = rawContent.map(parseMcpContentItem)
  const structuredJson = prettifyStructuredJson(result?.structuredContent)

  const errorObj = pickObject(item, 'error')
  const errorMessage = pickString(errorObj, 'message')
  const error = errorMessage.length > 0 ? errorMessage : undefined
  const durationMs = pickNumber(item, 'durationMs', undefined)

  return {
    server: pickString(item, 'server'),
    tool: pickString(item, 'tool', 'Tool'),
    argsJson,
    content,
    ...(structuredJson !== undefined ? { structuredJson } : {}),
    ...(error !== undefined ? { error } : {}),
    ...(failed ? { failed } : {}),
    ...(durationMs !== undefined ? { durationMs } : {}),
  }
}

function fromDynamicToolCall(item: Record<string, unknown>): McpCallFacts {
  const failed = item.status === 'failed' || item.status === 'cancelled' || item.status === 'declined' || undefined
  const argsJson = prettifyArgsJson(item.arguments)

  const items: unknown[] = Array.isArray(item.contentItems) ? item.contentItems : []
  const content: McpContentItem[] = items.flatMap((entry): McpContentItem[] => {
    if (!isObject(entry))
      return []
    if (entry.type === 'inputText' && typeof entry.text === 'string')
      return [{ type: 'text', text: entry.text }]
    const image = parseImageBlock(entry)
    if (image)
      return [{ type: 'image', source: image }]
    return [{ type: 'unknown', raw: entry }]
  })

  const durationMs = pickNumber(item, 'durationMs', undefined)
  return {
    server: pickString(item, 'namespace'),
    tool: pickString(item, 'tool', 'Tool'),
    argsJson,
    content,
    ...(failed ? { failed } : {}),
    ...(durationMs !== undefined ? { durationMs } : {}),
  }
}
