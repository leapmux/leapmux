import type { ToolCallRow } from '../../model/row'
import type { ToolKindMeta } from './renderer'
import { rowDrawsRequest, rowDrawsResult, rowHasResultRow } from '../../model/derivations'
import { isToolFailureResult, isUnparsedToolResult } from '../../model/toolCall'
import { contentBlocksCopyable } from '../genericToolCall'
import { textNeedsCollapse } from '../useCollapsedLines'
import { dispatchToolCall } from './index'

/**
 * What one row's toolbar offers, in the shape the bubble's outer toolbar reads.
 *
 * `copyableContent` is a getter, and `hasCopyable` is the presence answer the
 * toolbar reads to decide whether to draw the button at all. The two are ONE text:
 * `hasCopyable` must be true exactly when the getter returns a string, so it cannot
 * be a cheaper rule beside it. {@link copyableByRow} is what makes that affordable.
 */
export interface ToolResultMeta {
  /** The row holds more than it shows collapsed, so the toolbar offers Expand. */
  collapsible: boolean
  /** The row draws a diff, so the toolbar offers the split/unified toggle. */
  hasDiff: boolean
  /** True exactly when `copyableContent()` returns a string. */
  hasCopyable: boolean
  /** Lazily computed copyable text. Null when nothing is copyable. */
  copyableContent: () => string | null
}

export interface ToolCallMeta extends ToolResultMeta {
  /**
   * The words the Copy button states, from the side that stated the text.
   *
   * Undefined when the kind words nothing of its own, and each toolbar then states
   * its own last resort. BOTH toolbars read this field, so a kind that words its
   * button words it once -- see {@link toolCallMeta}.
   */
  copyLabel?: string
  /** The words the Expand button states. Undefined when the kind words nothing of its own. */
  expandLabel?: string
  previewText: () => string | null
}

/** The meta a ToolFailureResult or an UnparsedToolResult offers: one plain text block. */
export function plainMeta(text: string): ToolKindMeta {
  return {
    collapsible: textNeedsCollapse(text),
    hasDiff: false,
    copyableContent: () => text || null,
  }
}

/** The side of a row that stated its copyable text. */
type CopyableSide = 'primary' | 'request'

/** One row's copyable text, and the words that describe the side which stated it. */
interface RowCopyable {
  /** The text Copy writes and Quote quotes. Null when the row states none. */
  text: string | null
  /** The `copyLabel` of the side the text came from. Undefined when that side words none. */
  label: string | undefined
  /** The side the text came from. Null when neither side stated one. */
  side: CopyableSide | null
  /** False while the text is that side's stand-in (see `ToolKindMeta.copyableFinal`). */
  final: boolean
}

/**
 * One row's copyable text, built at most once for each revision of that row, and
 * once more when a stand-in turns final.
 *
 * `hasCopyable` is derived from the text, and the toolbar reads it on every reactive
 * pass -- so the text was built on every pass. For an edit row that is the
 * unified-diff format of every line of its diff, once per streamed token, for a body
 * nobody asked to copy. The ROW object is the revision: `cachedChatRow` replaces it
 * exactly when the row's content changes, and every other caller builds a fresh row,
 * so keying on it holds the build to once per change without a second rule that could
 * disagree with the text it describes.
 *
 * The entry wraps the value because `null` is a real answer -- the row is copyable-free
 * -- and must not read as a miss. It carries the LABEL for the same reason: which side
 * answered is known only while the text is built, and reading it a second time would
 * format that unified diff again.
 *
 * A side's stand-in (`ToolKindMeta.copyableFinal`) serves only until that side
 * answers final: an edit's diff arrives from the diff worker after the row mounts,
 * and the first read after it builds the final text.
 */
const copyableByRow = new WeakMap<ToolCallRow, RowCopyable>()

/**
 * ONE function for both toolbars (MessageBubble's outer one, ToolMessage's inner one).
 *
 * Every field a toolbar reads comes from here, INCLUDING `copyLabel` and
 * `expandLabel`, so a kind that words its own buttons words them once. What each
 * toolbar still owns is its own last resort for a kind that words nothing: the inner
 * one states `Expand output`, the outer one states `Expand`. Those two never describe
 * one button, because a row draws exactly one toolbar.
 */
export function toolCallMeta(row: ToolCallRow): ToolCallMeta {
  const call = row.call
  const drawsRequest = rowDrawsRequest(row)
  const drawsResult = rowDrawsResult(row)
  const plain = isToolFailureResult(call.result) || isUnparsedToolResult(call.result)
    ? call.result
    : undefined
  // The renderer and the call reach the hooks as ONE correlated pair, over the same
  // total table every reader dispatches through.
  const request = drawsRequest
    ? dispatchToolCall(call, ({ renderer, parsed }) => renderer.requestMeta?.(parsed, call.result !== undefined || rowHasResultRow(row)) ?? {})
    : {}
  const result = drawsResult
    ? dispatchToolCall(call, ({ renderer, resolved }) => resolved !== undefined
        ? renderer.resultMeta(resolved)
        : undefined)
    : undefined
  const fallback = drawsResult && plain
    ? plainMeta(plain.text)
    : undefined
  const primary = result ?? fallback
  // The meta of a side, from this call: a cached entry outlives the call that built it.
  const sideMeta = (side: CopyableSide | null): Partial<ToolKindMeta> | undefined => side === 'primary'
    ? primary
    : side === 'request'
      ? request
      : undefined
  // A side without `copyableFinal` states final text, and so does the extra
  // content that no side stated.
  const sideFinal = (side: CopyableSide | null): boolean => sideMeta(side)?.copyableFinal?.() ?? true
  // The BODY text and its words, built together. The primary side answers first and
  // the request side answers when it states nothing, so the label must follow the same
  // fallback: a command whose run printed nothing copies the COMMAND, and reading the
  // label off `primary` alone offered that command under the bare word "Copy".
  const build = (): RowCopyable => {
    const cached = copyableByRow.get(row)
    // A stand-in serves until its side answers final. That answer is reactive, so a
    // memo that reads the stand-in re-runs when its diff lands.
    if (cached !== undefined && (cached.final || !sideFinal(cached.side)))
      return cached
    const primaryText = primary?.copyableContent() ?? null
    const requestText = primaryText === null && drawsRequest
      ? request.copyableContent?.() ?? null
      : null
    const text = [
      primaryText ?? requestText,
      drawsResult
        ? contentBlocksCopyable(call.extraContent ?? [])
        : null,
    ].filter(Boolean).join('\n\n') || null
    // Neither side answered, so the text is the call's EXTRA content and neither
    // side's words describe it.
    const side: CopyableSide | null = primaryText !== null
      ? 'primary'
      : requestText !== null
        ? 'request'
        : null
    // Only the side that answered decides whether the text is final. A stand-in is
    // null exactly when its final text is, so the final text comes from the same
    // side, and `hasCopyable` and `copyLabel` already hold.
    const entry: RowCopyable = {
      text,
      label: sideMeta(side)?.copyLabel,
      side,
      final: sideFinal(side),
    }
    copyableByRow.set(row, entry)
    return entry
  }
  const copyableContent = (): string | null => build().text
  // One build answers both fields below. The getter reads the entry again, so a
  // stand-in that turns final later gives the final text.
  const built = build()
  // Expand opens whatever the row CLIPS, so the words come from the side that
  // holds it. Three cases, in this order:
  //
  //  1. The row drew a result body that CLIPS. That body is what the toggle opens,
  //     so it words the button -- and the request's words must not.
  //  2. The row drew a failure or an unparsed payload as plain text, and that text
  //     is long enough to clip. `plainMeta` words nothing, so the button keeps the
  //     toolbar's own last resort.
  //  3. Anything else can only clip the REQUEST: a paired request, a call that has
  //     not returned, a call whose turn ended before its result arrived, or a short
  //     result beside a long request summary. Reading the words off the result side
  //     alone left those rows stating the bare word "Expand" over the request.
  const expandLabel = result?.collapsible
    ? result.expandLabel
    : fallback?.collapsible ? fallback.expandLabel : request.expandLabel
  return {
    collapsible: (primary?.collapsible ?? false) || (drawsRequest && (request.collapsible ?? false)),
    hasDiff: (primary?.hasDiff ?? false) || (drawsRequest && (request.hasDiff ?? false)),
    copyableContent,
    hasCopyable: built.text !== null,
    // Absent stays absent rather than an explicitly undefined key, which each
    // toolbar's `??` last resort reads the same.
    ...(built.label !== undefined
      ? { copyLabel: built.label }
      : {}),
    ...(expandLabel !== undefined
      ? { expandLabel }
      : {}),
    // The request side answers when there is no primary, exactly as the two labels
    // above do. Consulting only `primary` made every request-side `previewText`
    // unreachable, so a pending edit's rail mark fell through to `copyableContent`
    // and showed a whole unified diff where the file paths belong.
    previewText: () => (primary
      ? primary.previewText?.()
      : request.previewText?.()) ?? copyableContent(),
  }
}
