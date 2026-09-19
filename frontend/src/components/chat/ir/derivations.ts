import type { McpContentItem } from './mcpToolCall'
import type { ChatRowIR, ToolCallRow, ToolRowPosition } from './row'
import type { ToolCallIR } from './toolCall'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { isGenericCall, typedResult } from './toolCall'

/** Pictures that ride in content blocks a generic result holds. */
function contentImages(content: readonly McpContentItem[]): ImageResultSource[] {
  return content.flatMap(item => item.type === 'image' ? [item.source] : [])
}

/** The pictures a call's EXTRA content carries, drawn above the result body. */
export function extraImages(call: ToolCallIR): ImageResultSource[] {
  return contentImages(call.extraContent ?? [])
}

/** Pictures a RESULT draws inline. Only the generic trio's content does. */
export function resultImages(call: ToolCallIR): ImageResultSource[] {
  // A predicate over the CALL, not over its kind: narrowing the discriminant leaves the
  // call itself un-narrowed, so the old form had to assert it back afterwards.
  const result = isGenericCall(call) ? typedResult(call) : undefined
  return result ? contentImages(result.content) : []
}

/**
 * Where one row sits in its span, on its own.
 *
 * A renderer view carries this WHOLE rather than restating `role` and the two flags,
 * so the union's rule -- a row is never its own sibling -- reaches every reader
 * instead of stopping at the row.
 */
export function toolRowPosition(row: ToolCallRow): ToolRowPosition {
  return row.role === 'request'
    ? { role: row.role, hasResultRow: row.hasResultRow }
    : row.role === 'result'
      ? { role: row.role, hasRequestRow: row.hasRequestRow }
      : { role: row.role, hasRequestRow: row.hasRequestRow, hasResultRow: row.hasResultRow }
}

/**
 * Whether the span's REQUEST is a row beside this one.
 *
 * Always false on the request itself, which the type states by leaving the flag out of
 * that branch. These two read the flag through one place, so no caller has to know
 * that an absent flag means no.
 */
export function rowHasRequestRow(row: ToolCallRow): boolean {
  return row.hasRequestRow ?? false
}

/** Whether the span's RESULT is a row beside this one. Always false on the result itself. */
export function rowHasResultRow(row: ToolCallRow): boolean {
  return row.hasResultRow ?? false
}

/** Whether this row draws the result: a result row, or a request/update row with no result row beside it. */
export function rowDrawsResult(row: ToolCallRow): boolean {
  return row.role === 'result' || !rowHasResultRow(row)
}

/**
 * Every image one row carries, in the order a reader sees them.
 *
 * An image TAB addresses a picture by its index in this list, so the order must
 * be the one the row draws -- and it survives a reload, when the tab resolves
 * the index against the message re-fetched from the worker. One derivation, so
 * index N in the tab and index N in the row cannot become two pictures.
 *
 * The order is the one `ToolMessage` lays out: the RESULT body first, the call's
 * EXTRA content under it, then the call's own images last. A list that led with the
 * extra content stated an order no row drew, so a tab opened from a result picture
 * showed the extra-content picture at the same index.
 *
 * A request row that has a result row beside it draws no result and lists no
 * images, or the pair would count the result's pictures twice.
 */
export function imagesForIR(row: ChatRowIR | null | undefined): ImageResultSource[] {
  if (row?.kind !== 'tool' || !rowDrawsResult(row))
    return []
  return [...resultImages(row.call), ...extraImages(row.call), ...row.call.images]
}

/**
 * The PROSE one row carries: what the agent said, what it thought, the plan it
 * proposed, what the reader typed. Copy-Markdown writes it, and Quote writes it
 * for every row that states one.
 *
 * A tool row states none here, and answers through a different mechanism. This
 * derivation reads the ROW, because a prose row holds its own text. A tool row's
 * text belongs to its KIND -- a diff, a command's output, a checklist -- so it comes
 * from `toolCallMeta(row).copyableContent()` in `~/components/chat/results/tools/meta.ts`,
 * the same getter the row's Copy button writes. `MessageBubble` reads this derivation
 * first and that getter second, which is what stops Copy and Quote stating two
 * different texts for one tool row.
 *
 * The two cannot become one function. This module is the IR layer, and the kind
 * metas sit above it in the renderer layer and import it, so calling them from here
 * would make the import cycle.
 */
export function quotableTextForIR(row: ChatRowIR | null | undefined): string | null {
  switch (row?.kind) {
    case 'assistant-text':
    case 'assistant-thinking':
    case 'assistant-plan':
    case 'plan-execution':
      return row.text.trim() || null
    case 'user':
      return row.text.trim() || null
    case 'compact-summary':
      return row.summary.trim() || null
    default:
      return null
  }
}
