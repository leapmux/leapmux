import type { McpContentItem, McpToolCallSource } from '../../../results/mcpToolCall'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { parseMcpContentItem } from '../../../results/mcpToolCall'
import { parseCodexStatus } from '../status'

/**
 * Build an `McpToolCallSource` from a Codex item. Handles both `mcpToolCall`
 * (server-bound MCP tools) and `dynamicToolCall` (function-style dynamic
 * tools). Returns null otherwise.
 *
 * Wire shapes (codex-rs/app-server-protocol/.../v2/ThreadItem.ts):
 * - mcpToolCall: { server, tool, status, arguments, result?, error?, durationMs? }
 *   where result = { content: JsonValue[], structuredContent, _meta }
 * - dynamicToolCall: { namespace?, tool, status, arguments, contentItems?, success?, durationMs? }
 *   where contentItems is { type: 'inputText'|'inputImage', ... }[]
 */
export function codexMcpFromItem(item: Record<string, unknown> | null | undefined): McpToolCallSource | null {
  if (!item)
    return null
  if (item.type === CODEX_ITEM.MCP_TOOL_CALL)
    return fromMcpToolCall(item)
  if (item.type === CODEX_ITEM.DYNAMIC_TOOL_CALL)
    return fromDynamicToolCall(item)
  return null
}

function fromMcpToolCall(item: Record<string, unknown>): McpToolCallSource {
  const status = parseCodexStatus(item.status)
  const argsJson = prettifyArgsJson(item.arguments)

  const result = pickObject(item, 'result')
  const rawContent = result && Array.isArray(result.content) ? result.content as unknown[] : []
  const content: McpContentItem[] = rawContent.map(parseMcpContentItem)
  const structuredJson = prettifyStructuredJson(result?.structuredContent)

  const errorObj = pickObject(item, 'error')
  const errorMessage = pickString(errorObj, 'message')
  const error = errorMessage.length > 0 ? errorMessage : undefined

  return {
    server: pickString(item, 'server'),
    tool: pickString(item, 'tool', 'Tool'),
    argsJson,
    content,
    structuredJson,
    error,
    status,
    durationMs: pickNumber(item, 'durationMs', undefined),
  }
}

function fromDynamicToolCall(item: Record<string, unknown>): McpToolCallSource {
  const status = parseCodexStatus(item.status)
  const argsJson = prettifyArgsJson(item.arguments)

  const items = Array.isArray(item.contentItems) ? item.contentItems as unknown[] : []
  const content: McpContentItem[] = items.flatMap((entry): McpContentItem[] => {
    if (!isObject(entry))
      return []
    const obj = entry as Record<string, unknown>
    if (obj.type === 'inputText' && typeof obj.text === 'string')
      return [{ type: 'text', text: obj.text as string }]
    if (obj.type === 'inputImage') {
      return [{
        type: 'image',
        urlOrData: pickString(obj, 'imageUrl', undefined),
      }]
    }
    return [{ type: 'unknown', raw: entry }]
  })

  // Dynamic tool calls have no separate error field; failure shows up via
  // status === 'failed' and the textual contentItems carry any details.
  const error = status === CODEX_STATUS.FAILED && content.length === 0 ? 'Tool call failed' : undefined

  return {
    server: pickString(item, 'namespace'),
    tool: pickString(item, 'tool', 'Tool'),
    argsJson,
    content,
    error,
    status,
    durationMs: pickNumber(item, 'durationMs', undefined),
  }
}
