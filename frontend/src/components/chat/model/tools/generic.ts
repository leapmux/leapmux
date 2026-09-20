import type { McpContentItem } from '../mcpToolCall'

/** The arguments as the provider sent them. The ONLY place raw arguments survive in the model. */
export interface GenericToolRequest { args: Record<string, unknown>, argsText?: string }
/** What a tool no vocabulary lists answers with: content blocks, as the MCP and ACP content schemas spell them. */
export interface GenericToolResult { content: McpContentItem[], structuredJson?: string, error?: string, durationMs?: number }

/**
 * Whether one result IS a generic one: the block list the uncategorized row draws.
 *
 * The degrade in `createToolCall` asks this. A draft it refuses still holds whatever the
 * tool produced, and the `other` kind can carry that verbatim when the shape already
 * matches -- which it does for the generic trio, whose three results are this one
 * type. A `FileChangeResult` cannot, so the degrade states words there instead.
 */
export function isGenericToolResult(value: unknown): value is GenericToolResult {
  return typeof value === 'object' && value !== null && Array.isArray((value as { content?: unknown }).content)
}
