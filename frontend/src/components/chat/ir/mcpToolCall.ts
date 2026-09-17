import type { ToolCallPayload } from './toolCall'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** A single MCP content item produced by the server. */
export type McpContentItem
  = | { type: 'text', text: string }
    | { type: 'image', source: ImageResultSource }
    | { type: 'resource', uri: string, mimeType?: string, text?: string }
    | { type: 'unknown', raw: unknown }

/**
 * What one Model Context Protocol call stated on the wire, before the payload
 * build folds it (Claude `mcp__server__tool`, Codex `mcpToolCall`, Codex
 * `dynamicToolCall`). The payload identifies the server and the tool on its request
 * and answers with the content blocks.
 */
export interface McpCallFacts {
  /** MCP server (or namespace) display name, e.g. `Tavily` / `siyuan`. */
  server: string
  /** Tool display name, e.g. `tavily_search`. */
  tool: string
  /** Pretty-JSON arguments, when the wire carries them as a string. */
  argsJson?: string
  /** Result content blocks. Empty when there's no result yet (in-progress) or on error. */
  content: McpContentItem[]
  /** Pretty-JSON `structuredContent` (Codex). Undefined when the server didn't send one. */
  structuredJson?: string
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
 * The request half of one Model Context Protocol call.
 *
 * Four providers hand-built this envelope and they had already drifted: three
 * carried a `label` of "MCP Tool Call" and Codex carried none, so the same kind of
 * call headed itself two different ways depending on which agent ran it. No label
 * is the answer -- `toolCallDisplayName` then falls through to the server and tool
 * pair, which identifies what actually ran.
 */
export function mcpToolCallRequest(server: string, tool: string, args: Record<string, unknown>): Pick<ToolCallPayload<'mcp'>, 'kind' | 'title' | 'request'> {
  return { kind: 'mcp', title: mcpToolCallDisplayName({ server, tool }), request: { server, tool, args } }
}

/**
 * Split an `mcp__server__tool` identifier into its two halves.
 *
 * This spelling is a Model Context Protocol convention rather than one agent's wire
 * shape: Claude Code and Reasonix both spell a tool this way, and each had its own
 * splitter. It lives beside {@link McpCallFacts}, whose two fields it fills.
 *
 * {@link splitPrefixedPair} is the mechanism, and it states how the halves split.
 * Returns null when the prefix is absent or EITHER half is empty -- an empty half
 * labels the row with nothing, which states less than the raw identifier does.
 */
export function parseMcpToolName(name: string): { server: string, tool: string } | null {
  return splitPrefixedPair(name, MCP_TOOL_NAME_PREFIX, '__')
}

/**
 * Split `<prefix><server><separator><tool>` into its two halves.
 *
 * The mechanism behind {@link parseMcpToolName}, and Reasonix calls it directly
 * with its own capability prefix and a `/` separator. Both identifiers hold a
 * server and a tool inside one string, and the only difference between them is
 * the two strings that delimit the halves.
 *
 * The TOOL half keeps every further separator, so `mcp__github__search__repos`
 * gives the server `github` and the tool `search__repos`. Returns null when the
 * prefix is absent, when the separator is absent, or when EITHER half is empty.
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
 * Best-effort parse of one MCP/JSON-RPC content block into our discriminated
 * union. Recognizes the standard shapes (`text`, `image`, `resource`) and
 * keeps anything else as `unknown` for raw-JSON display.
 */
export function parseMcpContentItem(raw: unknown): McpContentItem {
  if (!isObject(raw))
    return { type: 'unknown', raw }
  const obj = raw
  const t = pickString(obj, 'type')
  if (t === 'text' && typeof obj.text === 'string')
    return { type: 'text', text: obj.text as string }
  // `parseImageBlock` also accepts the Anthropic `source:{...}` shape, which
  // Claude tool_result content blocks use. This parser read only the flat
  // `data`/`url` keys, so an Anthropic-shaped image rendered as `[image]`.
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
