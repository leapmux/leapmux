import type { McpContentItem, McpToolCallSource, McpToolCallStatus } from '../../../results/mcpToolCall'
import { prettifyJson } from '~/lib/jsonFormat'
import { mcpToolCallDisplayName, parseMcpContentItem } from '../../../results/mcpToolCall'

const MCP_PREFIX = 'mcp__'

/** Tool name matches Claude's `mcp__server__tool` convention. */
export function isClaudeMcpTool(name: string): boolean {
  return name.startsWith(MCP_PREFIX)
}

/**
 * Split a Claude MCP tool name into `{ server, tool }`. The tool half preserves
 * any further `__` segments. Returns null when the name doesn't match.
 */
export function parseClaudeMcpToolName(name: string): { serverName: string, toolName: string } | null {
  const parts = name.split('__')
  const [mcpPart, serverName, ...toolNameParts] = parts
  if (mcpPart !== 'mcp' || !serverName)
    return null
  const toolName = toolNameParts.length > 0 ? toolNameParts.join('__') : undefined
  if (!toolName)
    return null
  return { serverName, toolName }
}

/** Capitalize underscore-separated server names: `claude_ai_Tavily` → `Claude Ai Tavily`. */
export function formatClaudeMcpServerName(serverName: string): string {
  return serverName
    .split('_')
    .map(w => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

/** Display name for an MCP tool, e.g. `Claude Ai Tavily / tavily_research`. */
export function formatClaudeMcpDisplayName(serverName: string, toolName: string): string {
  return mcpToolCallDisplayName({
    server: formatClaudeMcpServerName(serverName),
    tool: toolName,
  })
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
  const argsJson = args.toolInput && Object.keys(args.toolInput).length > 0
    ? prettifyJson(args.toolInput)
    : ''

  const content: McpContentItem[] = parseClaudeResultContent(args.resultContent)

  // Error message: when the call is flagged as an error, surface the joined
  // text content (Claude's MCP errors arrive as plain-text blocks).
  let error: string | undefined
  if (args.isError) {
    const flat = content
      .map(c => c.type === 'text' ? c.text : '')
      .filter(Boolean)
      .join('\n\n')
      .trim()
    error = flat || undefined
  }

  return {
    server: formatClaudeMcpServerName(parsed.serverName),
    tool: parsed.toolName,
    argsJson,
    // When the call is flagged as an error, drop content to avoid double-
    // rendering it (the error string already carries the text).
    content: args.isError ? [] : content,
    structuredJson: args.toolUseResult?.structuredContent && typeof args.toolUseResult.structuredContent === 'object'
      ? prettifyJson(args.toolUseResult.structuredContent)
      : undefined,
    error,
    status,
  }
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
