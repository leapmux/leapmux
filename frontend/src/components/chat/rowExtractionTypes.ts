import type { MessageCategory } from './messageClassifier'
import type { ToolSpanRowPresence } from './model/row'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import type { TodoItem } from '~/models/todo'

// These types describe extraction inputs. The model contains extraction outputs.
// Keep parsed provider payloads outside model/ so renderers cannot read native bytes.
// A renderer must use the neutral row model as its only input.

/** The provider's `spanRole` hook selects this message's role in its tool span. */
export type { ToolSpanRole } from '~/lib/messageSpan'

/**
 * A parse with the provider's supplemental content merged in, and nothing else.
 *
 * Only `resolveMessageForRendering` constructs this brand.
 * Classifiers and extractors must receive the resolved payload. Span-role readers require it also.
 * The brand prevents raw stored bytes from bypassing resolution.
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
  /** The current message's role, selected through its message ID. */
  role: ToolSpanRole
  /** The span rows that exist in the loaded transcript window. */
  visibleRows: ToolSpanRowPresence
}

/**
 * Everything one row extraction reads.
 *
 * The caller resolves the span's request and result once.
 * Plugins read those objects directly. They must not resolve sibling rows outside the caller's cache.
 */
export interface RowExtractionInput {
  /** The row's own content with its supplemental data merged: the resolved brand, by construction. */
  resolved: ResolvedMessageContent
  /** The classification the shared classifier already reached for this row. */
  category: MessageCategory
  /** The request and result of this row's tool span, plus this row's role. */
  span: ToolSpanContext
  /** The stored span ID selects this row's native call when a frame contains several calls. */
  spanId?: string
  /** The worker's `span_type` column, which identifies the tool on every span row. */
  spanType?: string
  /** LeapMux's own reading of how the row ended, which a provider frame can contradict. */
  completion?: MessageCompletion
  /** The immutable post-update task stored on this message. */
  todoSnapshot?: TodoItem
  /** Why a required task snapshot could not be read. */
  todoSnapshotDiagnostic?: string
}
