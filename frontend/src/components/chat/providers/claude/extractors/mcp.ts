import type { McpCallFacts, McpContentItem } from '../../../model/mcpToolCall'
import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { McpRequest } from '../../../model/tools/mcp'
import type { ClaudeToolRow } from './toolCommon'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { prettifyStructuredJson } from '~/lib/jsonFormat'
import { parseMcpContentItem, parseMcpToolName } from '../../../model/mcpToolCall'

/** Tool name matches the shared `mcp__server__tool` convention. */
export function isClaudeMcpTool(name: string): boolean {
  return parseMcpToolName(name) !== null
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
 * Read the wire facts of a Claude MCP tool_result. Returns null
 * when the tool name isn't an `mcp__server__tool` call.
 *
 * Claude doesn't carry a structured "MCP item" — the MCP-ness comes from the
 * tool name. Arguments are the linked tool_use input; result content is
 * Claude's standard `tool_result.content` array (text/image content blocks).
 */
export function claudeMcpFromToolResult(args: ClaudeMcpFromToolResultArgs): McpCallFacts | null {
  const parsed = parseMcpToolName(args.toolName)
  if (!parsed)
    return null

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

  const structuredJson = prettifyStructuredJson(args.toolUseResult?.structuredContent)
  return {
    server: parsed.server,
    tool: parsed.tool,
    // When the call is flagged as an error, drop the TEXT to avoid rendering
    // it twice -- the `error` string above is the joined text of these same
    // blocks. The images stay: nothing else carries them, so dropping them hid
    // the screenshot a failed MCP tool returned (Playwright returns one), and
    // it left the row with fewer images than `imagesForRow`
    // numbers for the message -- which is the index an already-open image tab
    // addresses by, permanently.
    content: args.isError ? content.filter(item => item.type !== 'text') : content,
    // Each optional half rides only when the wire stated it.
    ...(structuredJson !== undefined ? { structuredJson } : {}),
    ...(error !== undefined ? { error } : {}),
    ...(args.isError ? { failed: true } : {}),
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

/** The MCP pair: the server and tool the name spells, with the blocks it answered. */
export function claudeMcpSpec(request: McpRequest, args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'mcp'> {
  if (!result)
    return { kind: 'mcp', request }
  const source = claudeMcpFromToolResult({
    toolName: args.toolName,
    toolInput: args.input,
    // Absent, not null: the arg type takes null as a stated "no structured
    // payload", which a result row that carries no record does not state.
    ...(result.toolUseResult !== undefined ? { toolUseResult: result.toolUseResult } : {}),
    resultContent: result.rawResultContent,
    isError: result.isError === true,
  })
  const content = source?.content?.length
    ? source.content
    : result.images.map(image => ({ type: 'image' as const, source: image }))
  return {
    kind: 'mcp',
    request,
    result: {
      content,
      // Each optional half rides only when the facts carried one.
      ...(source?.structuredJson !== undefined ? { structuredJson: source.structuredJson } : {}),
      ...(source?.error !== undefined ? { error: source.error } : {}),
      ...(source?.durationMs !== undefined ? { durationMs: source.durationMs } : {}),
    },
  }
}
