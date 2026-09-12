import type { McpContentItem, McpToolCallSource, McpToolCallStatus } from '../../../results/mcpToolCall'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { prettifyArgsJson, prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { parseMcpContentItem } from '../../../results/mcpToolCall'
import { extractPairedToolUseInfo } from './assistantContent'

const MCP_PREFIX = 'mcp__'

/** Tool name matches Claude's `mcp__server__tool` convention. */
export function isClaudeMcpTool(name: string): boolean {
  return parseClaudeMcpToolName(name) !== null
}

/**
 * Split a Claude MCP tool name into `{ server, tool }`. The tool half preserves
 * any further `__` segments. Returns null when the name doesn't match.
 */
export function parseClaudeMcpToolName(name: string): { serverName: string, toolName: string } | null {
  if (!name.startsWith(MCP_PREFIX))
    return null
  const [, serverName, ...toolNameParts] = name.split('__')
  if (!serverName || toolNameParts.length === 0)
    return null
  const toolName = toolNameParts.join('__')
  if (!toolName)
    return null
  return { serverName, toolName }
}

interface ClaudeMcpFromToolResultArgs {
  toolName: string
  toolInput?: Record<string, unknown> | null
  toolUseResult?: Record<string, unknown> | null
  /** Raw `tool_result.content` — string or array of content blocks. */
  resultContent: unknown
  /** Whether the linked tool_result was flagged as an error. */
  isError?: boolean
}

/**
 * Build an `McpToolCallSource` from a Claude MCP tool_result. Returns null
 * when the tool name isn't an `mcp__server__tool` call.
 *
 * Claude doesn't carry a structured "MCP item" — the MCP-ness comes from the
 * tool name. Arguments are the linked tool_use input; result content is
 * Claude's standard `tool_result.content` array (text/image content blocks).
 */
export function claudeMcpFromToolResult(args: ClaudeMcpFromToolResultArgs): McpToolCallSource | null {
  const parsed = parseClaudeMcpToolName(args.toolName)
  if (!parsed)
    return null

  const status: McpToolCallStatus = args.isError ? 'failed' : 'completed'
  const argsJson = prettifyArgsJson(args.toolInput)

  const content: McpContentItem[] = parseClaudeResultContent(args.resultContent)

  // Error message: when the call is flagged as an error, surface the joined
  // text content (Claude's MCP errors arrive as plain-text blocks).
  let error: string | undefined
  if (args.isError) {
    const flat = joinContentParagraphs(
      content as Array<Record<string, unknown>>,
      { text: 'text' },
    ).trim()
    error = flat || undefined
  }

  return {
    server: parsed.serverName,
    tool: parsed.toolName,
    argsJson,
    // When the call is flagged as an error, drop the TEXT to avoid rendering
    // it twice -- the `error` string above is the joined text of these same
    // blocks. The images stay: nothing else carries them, so dropping them hid
    // the screenshot a failed MCP tool returned (Playwright returns one), and
    // it left the row with fewer images than `Provider.toolResultImages`
    // numbers for the message -- which is the index an already-open image tab
    // addresses by, permanently.
    content: args.isError ? content.filter(item => item.type !== 'text') : content,
    structuredJson: prettifyStructuredJson(args.toolUseResult?.structuredContent),
    error,
    status,
  }
}

/** Resolve the same MCP source for the result body and its toolbar. */
export function claudeMcpFromMessage(parsed: unknown, spanType: string | undefined, request: ParsedMessageContent | undefined): McpToolCallSource | null {
  if (!isObject(parsed))
    return null
  const requestInfo = extractPairedToolUseInfo(parsed, request)
  const toolUseResult = pickObject(parsed, 'tool_use_result')
  const toolName = spanType || pickString(toolUseResult, 'tool_name') || requestInfo?.toolName || ''
  if (!isClaudeMcpTool(toolName))
    return null
  const result = getMessageContent(parsed)?.find(block => isObject(block) && block.type === 'tool_result')
  if (!result)
    return null
  return claudeMcpFromToolResult({
    toolName,
    toolInput: requestInfo?.input,
    toolUseResult,
    resultContent: result.content,
    isError: result.is_error === true,
  })
}

function parseClaudeResultContent(raw: unknown): McpContentItem[] {
  if (typeof raw === 'string') {
    return raw.length > 0 ? [{ type: 'text', text: raw }] : []
  }
  if (!Array.isArray(raw))
    return []
  // Claude tool_result content blocks share the MCP shape (`{type, text}` /
  // `{type, ...}`), so the shared parser handles them.
  return raw.map(parseMcpContentItem)
}
