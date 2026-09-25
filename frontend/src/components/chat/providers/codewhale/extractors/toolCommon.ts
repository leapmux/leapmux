import type {} from '../../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { CODEWHALE_BLOCK_FIELD, CODEWHALE_BLOCK_TYPE, CODEWHALE_ENVELOPE_FIELD, CODEWHALE_EVENT, CODEWHALE_ITEM_FIELD, CODEWHALE_ITEM_METADATA, CODEWHALE_TOOL_FIELD, CODEWHALE_TRANSCRIPT_FIELD, CODEWHALE_TRANSCRIPT_KIND } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { parseToolOutcome } from '../../../toolOutcome'
import { retainedRowIsFinal } from '../../registry'
import { CODEWHALE_DEFERRED_LOAD_SENTENCE, CODEWHALE_RESULT_METADATA } from '../protocol'

/**
 * One runtime event, as the worker persisted it.
 *
 * The worker stores the envelope byte for byte, so the key names are the runtime's
 * own. `payload` is `{}` for an event that states none, which keeps every reader free
 * of a null check on a field the protocol always sends.
 */
export interface CodewhaleEnvelope {
  event: string
  turnId: string
  payload: Record<string, unknown>
}

/** Unwrap a persisted row into its runtime event, or null for a row in another shape. */
export function codewhaleEnvelope(parsed: unknown): CodewhaleEnvelope | null {
  if (!isObject(parsed))
    return null
  const event = pickString(parsed, CODEWHALE_ENVELOPE_FIELD.Event)
  if (!event)
    return null
  return {
    event,
    turnId: pickString(parsed, CODEWHALE_ENVELOPE_FIELD.TurnID),
    payload: pickObject(parsed, CODEWHALE_ENVELOPE_FIELD.Payload) ?? {},
  }
}

/** The four events that END an item, each with the outcome it states. */
const ITEM_FINAL_EVENTS: ReadonlyMap<string, CodewhaleItemOutcome> = new Map<string, CodewhaleItemOutcome>([
  [CODEWHALE_EVENT.ItemCompleted, 'completed'],
  [CODEWHALE_EVENT.ItemFailed, 'failed'],
  [CODEWHALE_EVENT.ItemInterrupted, 'interrupted'],
  [CODEWHALE_EVENT.ItemCanceled, 'interrupted'],
])

/**
 * How one item event ended its item: not at all, or with one of three outcomes.
 *
 * `item.interrupted` and `item.canceled` are one outcome to a reader. The runtime
 * sends the first for a turn the reader stopped and the second for a call it
 * withdrew, and both say that the call did not finish.
 */
export type CodewhaleItemOutcome = 'open' | 'completed' | 'failed' | 'interrupted'

/** The outcome an item event states. An event that is not an item event states none. */
export function codewhaleItemOutcome(event: string): CodewhaleItemOutcome | null {
  if (event === CODEWHALE_EVENT.ItemStarted)
    return 'open'
  return ITEM_FINAL_EVENTS.get(event) ?? null
}

/**
 * One turn item, the record an `item.*` event carries under `payload.item`.
 *
 * `detail` holds the item's WHOLE text: the reply of a message, the output of a tool,
 * the reason of a failure. `summary` repeats its head, cut at 280 characters, so no
 * reader takes a body from it.
 */
export interface CodewhaleItem {
  outcome: CodewhaleItemOutcome
  kind: string
  summary: string
  detail: string
  metadata: Record<string, unknown>
}

/** The item a row carries, or null for a row that is not an item event. */
export function codewhaleItem(parsed: unknown): CodewhaleItem | null {
  const envelope = codewhaleEnvelope(parsed)
  if (!envelope)
    return null
  const outcome = codewhaleItemOutcome(envelope.event)
  const item = pickObject(envelope.payload, CODEWHALE_ITEM_FIELD.Item)
  if (outcome === null || !item)
    return null
  return {
    outcome,
    kind: pickString(item, CODEWHALE_ITEM_FIELD.Kind),
    summary: pickString(item, CODEWHALE_ITEM_FIELD.Summary),
    detail: pickString(item, CODEWHALE_ITEM_FIELD.Detail),
    metadata: pickObject(item, CODEWHALE_ITEM_FIELD.Metadata) ?? {},
  }
}

/**
 * One frame of a tool call, from the main transcript or from a subagent's.
 *
 * The two transcripts state a call in different shapes. The main one sends turn items,
 * whose metadata identifies the tool. A subagent's transcript is the model's own
 * message history, which states a `tool_use` block and a `tool_result` block. This is
 * the one shape both read into, so every reader below it serves both.
 */
export interface CodewhaleToolFrame {
  /** The provider's id of the call, which is also the span id the worker opened. */
  callId: string
  /** The tool name, or `''` for a result block, which states none of its own. */
  toolName: string
  /** The call's arguments, or `{}` for a frame that states none. */
  input: Record<string, unknown>
  /** How this frame ended the call. `open` for the frame that opens it. */
  outcome: CodewhaleItemOutcome
  /** The words the call answered with: an item's `detail`, or a result block's content. */
  text: string
  /** The result metadata of an item, or `{}` for a block, which carries none. */
  metadata: Record<string, unknown>
  /**
   * The first call of a DEFERRED tool, which the runtime answered by loading the
   * tool's schema rather than by running it. The model calls the tool again, and that
   * second call is the real one, so both rows of the first call draw nothing.
   */
  deferredLoad: boolean
}

/**
 * The arguments an item states, parsed.
 *
 * An `item.started` carries them twice: parsed under `payload.tool.input`, and as a
 * JSON STRING under `metadata.tool_input`. A final event carries the string alone. The
 * parsed copy wins, and a string that does not parse into an object states none.
 */
function itemToolInput(payload: Record<string, unknown>, metadata: Record<string, unknown>): Record<string, unknown> {
  const parsed = pickObject(pickObject(payload, CODEWHALE_ITEM_FIELD.Tool), CODEWHALE_TOOL_FIELD.Input)
  if (parsed)
    return parsed
  const text = pickString(metadata, CODEWHALE_ITEM_METADATA.ToolInput)
  if (!text)
    return {}
  try {
    const value: unknown = JSON.parse(text)
    return isObject(value) ? value : {}
  }
  catch {
    return {}
  }
}

function itemToolFrame(envelope: CodewhaleEnvelope): CodewhaleToolFrame | null {
  const outcome = codewhaleItemOutcome(envelope.event)
  const item = pickObject(envelope.payload, CODEWHALE_ITEM_FIELD.Item)
  if (outcome === null || !item)
    return null
  const metadata = pickObject(item, CODEWHALE_ITEM_FIELD.Metadata) ?? {}
  const tool = pickObject(envelope.payload, CODEWHALE_ITEM_FIELD.Tool)
  const toolName = pickString(tool, CODEWHALE_TOOL_FIELD.Name) || pickString(metadata, CODEWHALE_ITEM_METADATA.ToolName)
  // A question's final event names its call `tool_call_id`, because the runtime
  // redacts the answer and rewrites the metadata. Every other event says `tool_use_id`.
  const callId = pickString(tool, CODEWHALE_TOOL_FIELD.ID)
    || pickString(metadata, CODEWHALE_ITEM_METADATA.ToolUseID)
    || pickString(metadata, CODEWHALE_ITEM_METADATA.ToolCallID)
  if (!toolName || !callId)
    return null
  return {
    callId,
    toolName,
    input: itemToolInput(envelope.payload, metadata),
    outcome,
    // An open item states its arguments in `detail`, which is not an answer.
    text: outcome === 'open' ? '' : pickString(item, CODEWHALE_ITEM_FIELD.Detail),
    metadata,
    deferredLoad: metadata[CODEWHALE_ITEM_METADATA.DeferredToolLoaded] === true,
  }
}

/**
 * One content block of a subagent transcript row, with the message it sits in.
 *
 * The worker stores one row for each block, and each row keeps the record's own
 * shape -- `{kind: "message", index, block, message: {role, content: [the block]}}` --
 * so a row never states an event the runtime did not write.
 */
export interface CodewhaleChildBlock {
  role: string
  type: string
  block: Record<string, unknown>
}

/** The block a subagent transcript row carries, or null for a row in another shape. */
export function codewhaleChildBlock(parsed: unknown): CodewhaleChildBlock | null {
  if (!isObject(parsed) || pickString(parsed, CODEWHALE_TRANSCRIPT_FIELD.Kind) !== CODEWHALE_TRANSCRIPT_KIND.Message)
    return null
  if (pickNumber(parsed, CODEWHALE_TRANSCRIPT_FIELD.Index) === null)
    return null
  const message = pickObject(parsed, CODEWHALE_TRANSCRIPT_FIELD.Message)
  const content = message?.[CODEWHALE_TRANSCRIPT_FIELD.Content]
  const block = Array.isArray(content) && content.length === 1 ? content[0] : undefined
  if (!isObject(block))
    return null
  const type = pickString(block, CODEWHALE_BLOCK_FIELD.Type)
  if (!type)
    return null
  return { role: pickString(message, CODEWHALE_TRANSCRIPT_FIELD.Role), type, block }
}

/** The text a tool result block answers with. Codewhale states it as a string. */
function blockResultText(block: Record<string, unknown>): string {
  const content = block[CODEWHALE_BLOCK_FIELD.Content]
  if (typeof content === 'string')
    return content
  // A block list is the other shape a tool result may take; its text parts are the answer.
  if (Array.isArray(content)) {
    return content
      .filter(isObject)
      .filter(part => pickString(part, CODEWHALE_BLOCK_FIELD.Type) === CODEWHALE_BLOCK_TYPE.Text)
      .map(part => pickString(part, CODEWHALE_BLOCK_FIELD.Text))
      .join('\n')
  }
  return ''
}

function blockToolFrame(child: CodewhaleChildBlock): CodewhaleToolFrame | null {
  const { block } = child
  if (child.type === CODEWHALE_BLOCK_TYPE.ToolUse) {
    const callId = pickString(block, CODEWHALE_BLOCK_FIELD.ID)
    const toolName = pickString(block, CODEWHALE_BLOCK_FIELD.Name)
    if (!callId || !toolName)
      return null
    return { callId, toolName, input: pickObject(block, CODEWHALE_BLOCK_FIELD.Input) ?? {}, outcome: 'open', text: '', metadata: {}, deferredLoad: false }
  }
  if (child.type === CODEWHALE_BLOCK_TYPE.ToolResult) {
    const callId = pickString(block, CODEWHALE_BLOCK_FIELD.ToolUseID)
    if (!callId)
      return null
    const text = blockResultText(block)
    return {
      callId,
      toolName: '',
      input: {},
      outcome: block[CODEWHALE_BLOCK_FIELD.IsError] === true ? 'failed' : 'completed',
      text,
      metadata: {},
      deferredLoad: CODEWHALE_DEFERRED_LOAD_SENTENCE.test(text),
    }
  }
  return null
}

/**
 * Whether a tool frame is a row that draws nothing, so the classifier hides it.
 *
 * Two result frames say so on their own, and the hide happens in the classifier
 * because only a classified-hidden row leaves the transcript. A row that the
 * extractor empties still takes a slot, and a slot of no height is never
 * measured, so it holds every later row of the transcript behind it.
 *
 * - The result of a deferred tool's FIRST call, which loaded the tool's schema
 *   and ran nothing. Its request row states the call and the runtime's words.
 * - The result of an answered question. The runtime redacts the answers from
 *   it, so it states nothing, and the saved answer beside it states the reply.
 */
export function codewhaleFrameDrawsNothing(frame: CodewhaleToolFrame): boolean {
  if (frame.outcome === 'open')
    return false
  if (frame.deferredLoad)
    return true
  return frame.outcome === 'completed' && frame.metadata[CODEWHALE_RESULT_METADATA.ResponseRedacted] === true
}

// Cache by row identity: the classifier, the span role and the extractor read the same
// frame, and the WeakMap releases an entry with its row.
const frameCache = new WeakMap<Record<string, unknown>, CodewhaleToolFrame | null>()

/**
 * The tool-call frame a row carries, from either transcript, or null for a row that is
 * not one.
 */
export function codewhaleToolFrame(parsed: unknown): CodewhaleToolFrame | null {
  if (!isObject(parsed))
    return null
  const cached = frameCache.get(parsed)
  if (cached !== undefined)
    return cached
  const envelope = codewhaleEnvelope(parsed)
  const child = envelope ? null : codewhaleChildBlock(parsed)
  const frame = envelope ? itemToolFrame(envelope) : child ? blockToolFrame(child) : null
  frameCache.set(parsed, frame)
  return frame
}

/**
 * Whether a tool frame FAILED, by any of the three ways the runtime states it.
 *
 * An `item.failed` event, a result block marked `is_error`, and a completed item whose
 * metadata reports `is_error: true` -- the runtime settles some refusals as a
 * completed call that carries the error.
 */
export function codewhaleFrameFailed(frame: CodewhaleToolFrame): boolean {
  return frame.outcome === 'failed' || frame.metadata[CODEWHALE_RESULT_METADATA.IsError] === true
}

/**
 * The role one tool frame plays in its span.
 *
 * A RETAINED row is final whatever its frame. A turn that ends while the call runs
 * stores the call's own opening frame, and LeapMux's completion column is what states
 * that the call did not finish. A row that carries a tool-outcome note is final for the
 * same reason.
 *
 * `parsed` is absent where a caller holds the row's bytes alone. The frame then decides.
 */
export function codewhaleToolSpanRole(frame: CodewhaleToolFrame, parsed: ParsedMessageContent | undefined): ToolSpanRole {
  if (retainedRowIsFinal(parsed?.completion) || parseToolOutcome(parsed?.messageMetadata) !== null)
    return 'result'
  return frame.outcome === 'open' ? 'request' : 'result'
}

/**
 * The paired frame, accepted only when it belongs to the same call.
 *
 * The store pairs rows by span id, which is the call id, so a mismatch means a row
 * from another call reached this one -- and reading it would put another call's
 * arguments or answer in this row.
 */
export function codewhalePairedFrame(own: CodewhaleToolFrame, paired: ParsedMessageContent | undefined): CodewhaleToolFrame | null {
  const frame = codewhaleToolFrame(paired?.parentObject)
  return frame && frame.callId === own.callId ? frame : null
}
