import type { MessageCategory } from './messageClassifier'
import type { ToolSpanRowPresence } from './model/row'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import type { TodoItem } from '~/models/todo'

// ---------------------------------------------------------------------------
// The INPUT side of row extraction.
//
// These types describe what an extractor reads: a parsed provider payload, the
// shared classification, the tool span, and an optional task snapshot. None of
// them is part of what extraction produces, and
// `model/row.ts` holds only the latter -- so a provider-neutral output model no
// longer has to import `ParsedMessageContent` for a field the renderer never
// sees.
//
// They sit above `model/` rather than inside it for the same reason: the model is the
// boundary the renderers read, and a renderer that can reach a raw parsed
// payload through it has a second route to the provider's wire format.
// ---------------------------------------------------------------------------

/** The role a MESSAGE plays in its tool span; the per-provider `spanRole` hook decides it. */
export type { ToolSpanRole } from '~/lib/messageSpan'

/**
 * A parse with the provider's supplemental content merged in, and nothing else.
 *
 * The BRAND is the compile-time boundary: only `resolveMessageForRendering()`
 * constructs this type, so a classifier, a span-role reader or an extractor
 * cannot accept the raw bytes the worker stored -- the merge has to have run.
 * `extractRow` therefore receives resolved content by construction.
 */
declare const resolvedMessageContent: unique symbol

export type ResolvedMessageContent
  = ParsedMessageContent & {
    readonly [resolvedMessageContent]: true
  }

/** Every side of one tool span the row's extractor reads, resolved once by the caller. */
export interface ToolSpanContext {
  /** The span's request, or undefined when the span states none. */
  request: ResolvedMessageContent | undefined
  /** The span's result, or undefined while the call still runs. */
  result: ResolvedMessageContent | undefined
  /** Where the CURRENT message sits in the span, decided by message id. */
  role: ToolSpanRole
  /** The span rows that exist in the loaded transcript window. */
  visibleRows: ToolSpanRowPresence
}

/**
 * Everything one row extraction reads.
 *
 * `sides` arrives already resolved, so a plugin never reaches back into
 * `context.sources` for a side the caller had in hand -- each read there is a fresh
 * resolution outside the memo that produced this input.
 */
export interface RowExtractionInput {
  /** The row's own content with its supplemental data merged: the resolved brand, by construction. */
  resolved: ResolvedMessageContent
  /** The classification the shared classifier already reached for this row. */
  category: MessageCategory
  /** The request and result of this row's tool span, plus this row's role. */
  span: ToolSpanContext
  /** The worker's `span_type` column, which identifies the tool on every span row. */
  spanType?: string
  /** LeapMux's own reading of how the row ended, which a provider frame can contradict. */
  completion?: MessageCompletion
  /** The immutable post-update task stored on this message. */
  todoSnapshot?: TodoItem
  /** Why a required task snapshot could not be read. */
  todoSnapshotDiagnostic?: string
}
