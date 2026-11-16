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

/**
 * The side of one tool span that a row's extractor may read, resolved once by the
 * caller.
 *
 * A row reads its own message and the EARLIER request of its span, and never a later
 * message. The result of a span arrives after its request, so a request row that read
 * it could not show, nor report a final height, until the result existed: the browser
 * would wait for it, or fetch it. That is why this type has no member for the result.
 * A plugin cannot read one, because no input carries it.
 */
export interface ToolSpanContext {
  /**
   * The span's request, or undefined when the span states none or the browser does not
   * hold it. A request row receives its own message here.
   */
  request: ResolvedMessageContent | undefined
  /** The current message's role, selected through its message ID. */
  role: ToolSpanRole
  /** Whether the loaded transcript window draws the span's request as a row of its own. */
  visibleRows: ToolSpanRowPresence
}

/**
 * Everything one row extraction reads.
 *
 * The caller resolves the span's request once.
 * Plugins read that object directly. They must not resolve sibling rows outside the caller's cache.
 */
export interface RowExtractionInput {
  /** The row's own content with its supplemental data merged: the resolved brand, by construction. */
  resolved: ResolvedMessageContent
  /** The classification the shared classifier already reached for this row. */
  category: MessageCategory
  /** The request of this row's tool span, plus this row's role. No later message of the span. */
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
