import type { ControlResponseDisplay } from '../persistedControlResponse'
import type { AgentPromptIR, DividerIR } from './divider'
import type { NotificationThreadIR } from './notification'
import type { ToolCallIR } from './toolCall'

/** One file a user attached to a message they sent. */
export interface AttachmentIR {
  filename?: string
  mimeType?: string
}

/** Where one row sits in its tool span. */
export type ToolRowRole = 'request' | 'update' | 'result'

/**
 * Where one row sits in its span, and which SIBLING rows the transcript draws beside it.
 *
 * A row is never its own sibling, so each role states only the siblings it can have:
 * the request is its own row, and the result is its own row. Writing the union
 * this way is what makes `{ role: 'request', hasRequestRow: true }` -- a row that
 * claims to sit beside itself -- impossible to spell. A reader still asks for either
 * flag on any row and gets `undefined`, which is falsy, where the role rules it out.
 */
export type ToolRowPosition
  = | { role: 'request', hasRequestRow?: false, hasResultRow: boolean }
    | { role: 'update', hasRequestRow: boolean, hasResultRow: boolean }
    | { role: 'result', hasRequestRow: boolean, hasResultRow?: false }

/** What the transcript draws for one tool SPAN, whichever row the extraction builds. */
export interface ToolSpanRows {
  /** The span states a request, and the transcript draws it as its own row. */
  request: boolean
  /** The span states a result, and the transcript draws it as its own row. */
  result: boolean
}

/** One tool row as ONE merged call, plus where this row sits in the span. */
export type ToolCallRow = {
  kind: 'tool'
  /** The call, built from every side the store resolved for this span. */
  call: ToolCallIR
} & ToolRowPosition

/**
 * Build one tool row from what the SPAN holds.
 *
 * Six providers each restated `!!side && role !== 'request'` to fill the two flags.
 * The rule is one rule -- a row is never its own sibling -- so it belongs here: a
 * provider that restated it wrongly drew a result row with its own header suppressed
 * and no request row beside it to carry one, and nothing failed.
 */
export function toolCallRow(call: ToolCallIR, role: ToolRowRole, span: ToolSpanRows): ToolCallRow {
  switch (role) {
    case 'request':
      return { kind: 'tool', call, role, hasResultRow: span.result }
    case 'result':
      return { kind: 'tool', call, role, hasRequestRow: span.request }
    default:
      return { kind: 'tool', call, role, hasRequestRow: span.request, hasResultRow: span.result }
  }
}

/**
 * Every row the transcript can draw, after its provider read its own wire format.
 *
 * Layer 2 of the render pipeline. A provider plugin produces one of these from its
 * own bytes, and the shared renderer switches on `kind` -- so no shared module
 * branches on a provider, a tool name, or an envelope shape.
 *
 * `unrecognized` is the safety net rather than a failure: a provider that cannot read
 * one of its own frames returns it, and the reader gets the frame in a collapsed card
 * instead of a blank row.
 */
export type ChatRowIR
  = | ToolCallRow
    | { kind: 'notification', thread: NotificationThreadIR }
    | { kind: 'divider', divider: DividerIR }
    | { kind: 'assistant-text', text: string }
    | { kind: 'assistant-thinking', text: string }
    | { kind: 'assistant-plan', text: string }
    | { kind: 'user', text: string, attachments: AttachmentIR[] }
    | { kind: 'agent-prompt', prompt: AgentPromptIR }
    | { kind: 'plan-execution', text: string }
    | { kind: 'compact-summary', summary: string }
    /**
     * A saved answer to a control request, already reduced to the words a reader
     * sees.
     *
     * The native request and response objects stop here. Layer 1 runs the provider's
     * own `controlResponseDisplay` and keeps only its answer, so the transcript row
     * and the scroll-rail dot draw ONE derivation rather than each running the
     * provider hook again -- and no reader of a row holds a provider's wire bytes.
     */
    | { kind: 'control-response', display: ControlResponseDisplay }
    | { kind: 'hidden' }
    | { kind: 'unrecognized', payload: unknown }
