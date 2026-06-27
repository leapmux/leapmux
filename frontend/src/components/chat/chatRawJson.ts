import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'

function jsonInt64(value: bigint): number | string {
  return value >= BigInt(Number.MIN_SAFE_INTEGER) && value <= BigInt(Number.MAX_SAFE_INTEGER)
    ? Number(value)
    : value.toString()
}

/**
 * Reconstruct a message's full wire envelope as a JSON string for the raw-JSON debug
 * surface (the `hidden` / `unsupported_provider` rows, whose whole purpose is to show
 * the bytes when something is wrong). Mirrors the persisted shape field-for-field,
 * omitting proto3 zero-value fields so the output matches what the backend stored.
 *
 * Two parse guards (span_lines and content) degrade a corrupt payload to its raw
 * string rather than throwing into the ErrorBoundary and hiding the very bytes this
 * view exists to inspect. `sourceName` is the display label for `message.source`
 * (the caller's sourceLabel), passed in so this stays a pure, UI-free function.
 *
 * `heights` (optional) carries the measured DOM height for this row as
 * `geometry.height`. It is omitted until the row has a real DOM measurement.
 */
export function buildRawJsonEnvelope(
  message: AgentChatMessage,
  parsed: ParsedMessageContent,
  sourceName: string,
  heights?: { measured?: number },
): string {
  const envelope: Record<string, unknown> = {
    id: message.id,
    source: sourceName,
    seq: jsonInt64(message.seq),
    created_at: message.createdAt,
  }
  if (message.deliveryError)
    envelope.delivery_error = message.deliveryError
  if (message.depth)
    envelope.depth = message.depth
  if (message.spanId)
    envelope.span_id = message.spanId
  if (message.parentSpanId)
    envelope.parent_span_id = message.parentSpanId
  if (message.spanType)
    envelope.span_type = message.spanType
  if (message.spanColor > 0)
    envelope.span_color = message.spanColor
  if (message.spanLines && message.spanLines !== '[]') {
    // span_lines is backend-generated JSON, but a corrupt value must still render:
    // degrade to its raw string instead of throwing (this is the debug surface).
    try {
      envelope.span_lines = JSON.parse(message.spanLines)
    }
    catch {
      envelope.span_lines = message.spanLines
    }
  }
  if (parsed.wrapper && parsed.wrapper.old_seqs.length > 0)
    envelope.old_seqs = parsed.wrapper.old_seqs

  // Debug geometry: the row's measured DOM height. A `geometry` namespace (not
  // a flat field) leaves room for future per-row geometry (offset, width).
  if (heights?.measured !== undefined) {
    envelope.geometry = { height: heights.measured }
  }

  if (parsed.wrapper) {
    envelope.messages = parsed.wrapper.messages
    return JSON.stringify(envelope)
  }

  try {
    envelope.content = JSON.parse(parsed.rawText)
    return JSON.stringify(envelope)
  }
  catch {
    // A non-JSON content payload (or a corrupt one): fall back to the raw text so
    // the debug view still shows the bytes rather than throwing.
    return parsed.rawText
  }
}
