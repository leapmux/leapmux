import type { McpContentItem } from '../mcpToolCall'
import { prettifyJson } from '~/lib/jsonFormat'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../collapse'

/** The arguments as the provider sent them. The ONLY place raw arguments survive in the IR. */
export interface GenericRequest { args: Record<string, unknown>, argsText?: string }
/** What a tool no vocabulary lists answers with: content blocks, as the MCP and ACP content schemas spell them. */
export interface GenericResult { content: McpContentItem[], structuredJson?: string, error?: string, durationMs?: number }

function contentText(item: McpContentItem): string {
  switch (item.type) {
    case 'text': return item.text
    case 'resource': return [item.uri, item.text].filter(value => value !== undefined).join('\n')
    case 'unknown': return prettifyJson(item.raw)
    case 'image': return item.source.description ?? ''
  }
}

/**
 * Whether one result IS a generic one: the block list the uncategorized row draws.
 *
 * The degrade in `toolCall` asks this. A draft it refuses still holds whatever the
 * tool produced, and the `other` kind can carry that verbatim when the shape already
 * matches -- which it does for the generic trio, whose three results are this one
 * type. A `FileChangeResult` cannot, so the degrade states words there instead.
 */
export function isGenericResult(value: unknown): value is GenericResult {
  return typeof value === 'object' && value !== null && Array.isArray((value as { content?: unknown }).content)
}

/**
 * The text the Copy action writes for a list of content blocks.
 *
 * Apart from {@link genericResultCopyable}, because a call's EXTRA content is the
 * same block list and is not a generic result. The reader that wanted these words
 * used to build a `GenericResult` around the list to reach them, which claimed a
 * shape the call never had.
 */
export function contentBlocksCopyable(content: readonly McpContentItem[]): string {
  return content.map(contentText).filter(Boolean).join('\n\n')
}

/** The text the Copy action writes for a generic result: every content block, the structured payload, the error. */
export function genericResultCopyable(result: GenericResult): string {
  return [contentBlocksCopyable(result.content), result.structuredJson, result.error].filter(Boolean).join('\n\n')
}

/**
 * Whether a generic result holds more than the collapsed row shows.
 *
 * Takes the arguments as the SECOND parameter, because the collapse decision reads
 * them (a long argument list is as expandable as a long result) and the arguments
 * live on the REQUEST, not the result.
 */
export function genericResultCollapsible(result: GenericResult, argsJson: string): boolean {
  return [argsJson, result.structuredJson, result.error, ...result.content.map(item => item.type === 'image' ? undefined : item.type === 'resource' ? item.text : contentText(item))]
    .some(text => text !== undefined && hasMoreLinesThan(text, COLLAPSED_RESULT_ROWS))
}
