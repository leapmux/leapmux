import type { McpContentItem, StructuredJsonRole } from '../mcpToolCall'

/** The original tool arguments. This request is the only model field that stores raw arguments. */
export interface GenericToolRequest { args: Record<string, unknown>, argsText?: string }
/** Returned content blocks with optional structured data, execution metadata, and error text. */
export interface GenericToolResult { content: McpContentItem[], structuredJson?: string, structuredJsonRole?: StructuredJsonRole, error?: string, durationMs?: number }

/**
 * Identify the content-block shape used by generic results.
 * A rejected draft can preserve this shape under the other kind.
 * A file-change result uses another shape and cannot pass this check.
 */
export function isGenericToolResult(value: unknown): value is GenericToolResult {
  return typeof value === 'object' && value !== null && 'content' in value && Array.isArray(value.content)
}
