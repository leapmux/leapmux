import type { AssistantTextSegment } from './assistantText'
import type { ControlResponseSummary } from './controlResponse'
import type { TurnEnd } from './divider'
import type { NotificationThread } from './notification'
import type { ToolCall } from './toolCall'

/** One file a user attached to a message they sent. */
export interface UserMessageAttachment {
  filename?: string
  mimeType?: string
}

/** Where one row sits in its tool span. */
export type ToolSpanRowRole = 'request' | 'update' | 'result'

/**
 * Where one row sits in its span, and whether the transcript draws the span's REQUEST
 * as a row beside it.
 *
 * A row knows nothing about the rows that come AFTER it in its span. A request row
 * never learns whether its result exists: its height must be final when it joins the
 * list, and a result arrives later. So no role carries a flag about the result row.
 *
 * A row is never its own sibling, so the request role states no request flag. Writing
 * the union this way makes `{ role: 'request', hasRequestRow: true }` -- a row that
 * claims to sit beside itself -- impossible to spell. A reader still asks for the
 * flag on any row and gets `undefined`, which is falsy, where the role rules it out.
 */
export type ToolSpanRowPosition
  = | { role: 'request', hasRequestRow?: false }
    | { role: 'update', hasRequestRow: boolean }
    | { role: 'result', hasRequestRow: boolean }

/**
 * What the transcript draws for the REQUEST of one tool span, whichever row the
 * extraction builds.
 *
 * It states nothing about the result. A row reads only its own message and the
 * EARLIER request of its span (see ToolSpanContext).
 */
export interface ToolSpanRowPresence {
  /** The span states a request, and the transcript draws it as its own row. */
  request: boolean
}

/** One tool row as ONE call, plus where this row sits in the span. */
export type ToolCallRow = {
  kind: 'tool'
  /**
   * The call, built from this row's own message and the earlier request of its span.
   * A request row's call holds no result: no input of the extraction carries one.
   */
  call: ToolCall
} & ToolSpanRowPosition

/**
 * Build one tool row from what the SPAN holds.
 *
 * Six providers each restated `!!side && role !== 'request'` to fill the flag. The
 * rule is one rule -- a row is never its own sibling -- so it belongs here: a provider
 * that restated it wrongly drew a result row with its own header suppressed and no
 * request row beside it to carry one, and nothing failed.
 */
export function toolCallRow(call: ToolCall, role: ToolSpanRowRole, span: ToolSpanRowPresence): ToolCallRow {
  switch (role) {
    case 'request':
      return {
        kind: 'tool',
        call,
        role,
      }
    case 'result':
      return {
        kind: 'tool',
        call,
        role,
        hasRequestRow: span.request,
      }
    default:
      return {
        kind: 'tool',
        call,
        role,
        hasRequestRow: span.request,
      }
  }
}

/**
 * Every row the transcript can draw, after its provider read its own wire format.
 *
 * Layer 2 of the render pipeline. A provider plugin produces one of these from its
 * own bytes, and the shared renderer switches on `kind` -- so no shared module
 * branches on a provider, a tool name, or an envelope shape.
 *
 * Unsupported and failed extraction stay outside this union as
 * `ChatRowExtraction` outcomes. The model therefore contains only rows that a
 * provider or LeapMux read successfully.
 */
export type ChatRow
  = | ToolCallRow
    | { kind: 'notification', thread: NotificationThread }
    | { kind: 'divider', divider: TurnEnd }
    | { kind: 'assistant-text', text: string, segments?: AssistantTextSegment[] }
    | { kind: 'assistant-thinking', text: string }
    | { kind: 'assistant-plan', text: string }
    | { kind: 'user', text: string, attachments: UserMessageAttachment[] }
    | { kind: 'plan-execution', text: string }
    /**
     * A saved answer to a control request, already reduced to the words a reader
     * sees.
     *
     * The native request and response objects stop here. Layer 1 runs the provider's
     * own `controlResponseDisplay` and keeps only its answer, so the transcript row
     * and the scroll-rail dot draw ONE derivation rather than each running the
     * provider hook again -- and no reader of a row holds a provider's wire bytes.
     */
    | { kind: 'control-response', display: ControlResponseSummary }
    | { kind: 'hidden' }
