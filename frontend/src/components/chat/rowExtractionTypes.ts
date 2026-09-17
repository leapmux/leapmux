import type { MessageCategory } from './messageClassification'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/models/todo'

// ---------------------------------------------------------------------------
// The INPUT side of row extraction.
//
// These types describe what an extractor READS: a parsed provider payload, the
// classification the shared classifier reached, the sides of a tool span, the
// live to-do list. None of them is part of what extraction PRODUCES, and
// `ir/row.ts` holds only the latter -- so a provider-neutral output model no
// longer has to import `ParsedMessageContent` for a field the renderer never
// sees.
//
// They sit above `ir/` rather than inside it for the same reason: the IR is the
// boundary the renderers read, and a renderer that can reach a raw parsed
// payload through it has a second route to the provider's wire format.
// ---------------------------------------------------------------------------

/**
 * The role a MESSAGE plays in a tool span: the opener that states the call, the
 * result that completes it, or neither. Providers mark opener vs result
 * differently, so the classifier is the per-provider `spanRole` hook.
 */
export type SpanRole = 'opener' | 'result' | 'other'

/** Every side of one tool span the row's extractor reads, resolved once by the caller. */
export interface ToolSpanSides {
  /** The message the extractor was called for. */
  current: ParsedMessageContent | undefined
  /** The span's opener, or undefined when the span states none. */
  request: ParsedMessageContent | undefined
  /** The span's result, or undefined while the call still runs. */
  result: ParsedMessageContent | undefined
  /** Where the CURRENT message sits in the span, decided by message id. */
  role: SpanRole
}

/**
 * Everything one row extraction reads.
 *
 * `sides` arrives already resolved, so a plugin never reaches back into
 * `context.sources` for a side the caller had in hand -- each read there is a fresh
 * resolution outside the memo that produced this input.
 */
export interface RowExtractionInput {
  /** The row's own parsed content, with its supplemental data already merged. */
  parsed: ParsedMessageContent
  /** The classification the shared classifier already reached for this row. */
  category: MessageCategory
  /** The three sides of this row's tool span, plus this row's place among them. */
  sides: ToolSpanSides
  /** The worker's `span_type` column, which identifies the tool on every span row. */
  spanType?: string
  /** LeapMux's own reading of how the row ended, which a provider frame can contradict. */
  completion?: MessageCompletion
  /**
   * The live to-do store, by task id.
   *
   * Session metadata rather than row content, and it arrives here for one reason:
   * a provider that sends a PATCH -- Claude's `TaskUpdate` -- states only the
   * fields that changed, so the row has to read the rest from the list the store
   * already holds. Without it a patch that moved a task to `completed` drew
   * `Task #<id>` where the subject belongs.
   */
  todoById?: (taskId: string) => TodoItem | undefined
}
