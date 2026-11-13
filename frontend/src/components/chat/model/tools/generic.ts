import type { McpContentItem, StructuredJsonRole } from '../mcpToolCall'
import { prettifyArgsJson, prettifyJson } from '~/lib/jsonFormat'
import { memoizeByObject } from '~/lib/memoizeByObject'

/** The original tool arguments. This request is the only model field that stores raw arguments. */
export interface GenericToolRequest { args: Record<string, unknown>, argsText?: string }

/**
 * The formatted arguments of each request. The model rebuilds a request when its
 * row's revision changes, so an entry expires exactly when the arguments can differ.
 */
const formattedArgs = memoizeByObject((request: GenericToolRequest) => prettifyArgsJson(request.args))

/**
 * The arguments of a generic call as the row shows and measures them: the text the
 * provider stated, or else the formatted JSON of the arguments, or else nothing.
 *
 * Formatted once per request. The body, its collapse test and the toolbar read it on
 * every reactive pass, and formatting large arguments is the costly part.
 */
export function genericArgsText(request: GenericToolRequest): string {
  return request.argsText ?? formattedArgs(request)
}

/** A returned content block whose shape the model does not know. */
type UnknownContentItem = Extract<McpContentItem, { type: 'unknown' }>

/**
 * The text that a generic row shows, measures and copies for a content block of an
 * unknown shape: its raw value, formatted as JSON.
 *
 * Formatted once per item, as the arguments are. The body, its collapse test and
 * Copy each read it, and formatting a large value is the costly part. The model
 * rebuilds the items when the row's revision changes, so an entry expires exactly
 * when the value can differ.
 */
export const unknownContentText = memoizeByObject((item: UnknownContentItem) => prettifyJson(item.raw))

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
