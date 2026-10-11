import type { ToolCallSpec } from './toolCall'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** The role of a structuredJson value. 'metadata' identifies source-defined execution metadata. An absent role identifies returned tool output. */
export type StructuredJsonRole = 'metadata'

/** A single Model Context Protocol (MCP) content item from the server. */
export type McpContentItem
  = | { type: 'text', text: string }
    | { type: 'image', source: ImageResultSource }
    | { type: 'resource', uri: string, mimeType?: string, text?: string }
    | { type: 'unknown', raw: unknown }

/**
 * Neutral facts from one Model Context Protocol call.
 * The request identifies the server and tool.
 * The result supplies content blocks and optional structured data.
 */
export interface McpCallFacts {
  /** MCP server (or namespace) display name, e.g. `Tavily` / `siyuan`. */
  server: string
  /** Tool display name, e.g. `tavily_search`. */
  tool: string
  /** Pretty-JSON arguments, when the wire carries them as a string. */
  argsJson?: string
  /** Returned content blocks. Empty before the result arrives, when the call fails, or when the tool returns no content. */
  content: McpContentItem[]
  /** Formatted JSON from structured output or source-defined execution metadata. Undefined when the source sends neither. */
  structuredJson?: string
  /**
   * Set 'metadata' when structuredJson holds source-defined execution metadata.
   * The renderer draws that value before the returned output and does not mark it as output. Copy keeps its original order.
   */
  structuredJsonRole?: StructuredJsonRole
  /** Error message when the call failed. */
  error?: string
  /** The call reported itself failed. A cancelled or declined call reads the same. */
  failed?: boolean
  /** Duration in milliseconds, when the agent reports it. */
  durationMs?: number
}

/** Display name fragment: "Server / tool" (or just "tool" when server is empty). */
export function mcpToolCallDisplayName(source: { server: string, tool: string }): string {
  return source.server ? `${source.server} / ${source.tool}` : source.tool
}

/** The prefix an `mcp__server__tool` identifier carries. */
const MCP_TOOL_NAME_PREFIX = 'mcp__'

/**
 * Build one Model Context Protocol request.
 * The server and tool identify the display title.
 * Omit a separate label so every provider uses that same title.
 */
export function mcpToolCallRequest(server: string, tool: string, args: Record<string, unknown>): Pick<ToolCallSpec<'mcp'>, 'kind' | 'title' | 'request'> {
  return { kind: 'mcp', title: mcpToolCallDisplayName({ server, tool }), request: { server, tool, args } }
}

/**
 * Split the server and tool in an `mcp__server__tool` identifier.
 * This spelling is a shared Model Context Protocol convention. Claude Code and Reasonix both use it.
 * Return null when the prefix is missing or the server or tool is empty.
 * An empty part would label the row with no text, which states less than the raw identifier.
 */
export function parseMcpToolName(name: string): { server: string, tool: string } | null {
  return splitPrefixedPair(name, MCP_TOOL_NAME_PREFIX, '__')
}

/**
 * Split an identifier with its specified prefix and separator.
 * The tool retains every later separator: `mcp__github__search__repos` gives the server `github` and the tool `search__repos`.
 * Reasonix calls this function directly with its capability prefix and a `/` separator.
 * Return null when the prefix or separator is missing, or when the server or tool is empty.
 */
export function splitPrefixedPair(id: string, prefix: string, separator: string): { server: string, tool: string } | null {
  if (!id.startsWith(prefix))
    return null
  const index = id.indexOf(separator, prefix.length)
  if (index < 0)
    return null
  const server = id.slice(prefix.length, index)
  const tool = id.slice(index + separator.length)
  return server && tool ? { server, tool } : null
}

/**
 * Read one MCP content block into its neutral type.
 * Recognize text, images, and resources.
 * Preserve every other shape as unknown content for JSON display.
 */
export function parseMcpContentItem(raw: unknown): McpContentItem {
  if (!isObject(raw))
    return { type: 'unknown', raw }
  const obj = raw
  const t = pickString(obj, 'type')
  const text = obj.text
  if (t === 'text' && typeof text === 'string')
    return { type: 'text', text }
  // The shared image reader accepts flat data and URL fields.
  // It also accepts the nested source object used by Anthropic content blocks.
  const image = parseImageBlock(obj)
  if (image)
    return { type: 'image', source: image }
  const resource = t === 'resource' ? pickObject(obj, 'resource') ?? obj : undefined
  if (resource && typeof resource.uri === 'string' && !('blob' in resource)) {
    const mimeType = pickString(resource, 'mimeType', undefined)
    return {
      type: 'resource',
      uri: resource.uri,
      ...(mimeType !== undefined ? { mimeType } : {}),
      ...(typeof resource.text === 'string' ? { text: resource.text } : {}),
    }
  }
  return { type: 'unknown', raw }
}
