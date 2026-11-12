import type { McpContentItem, StructuredJsonRole } from '../mcpToolCall'
import { prettifyJson } from '~/lib/jsonFormat'

/** The original tool arguments. This request is the only model field that stores raw arguments. */
export interface GenericToolRequest { args: Record<string, unknown>, argsText?: string }

/**
 * The formatted arguments of each request. The model rebuilds a request when its
 * row's revision changes, so an entry expires exactly when the arguments can differ.
 */
const argsTextByRequest = new WeakMap<GenericToolRequest, string>()

/**
 * The arguments of a generic call as the row shows and measures them: the text the
 * provider stated, or else the formatted JSON of the arguments, or else nothing.
 *
 * Formatted once per request. The body, its collapse test and the toolbar read it on
 * every reactive pass, and formatting large arguments is the costly part.
 */
export function genericArgsText(request: GenericToolRequest): string {
  if (request.argsText !== undefined)
    return request.argsText
  const cached = argsTextByRequest.get(request)
  if (cached !== undefined)
    return cached
  const text = Object.keys(request.args).length > 0
    ? prettifyJson(request.args)
    : ''
  argsTextByRequest.set(request, text)
  return text
}
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
