import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { messageCompletionFromProto } from './assembledMessage'

function jsonInt64(value: bigint): number | string {
  return value >= BigInt(Number.MIN_SAFE_INTEGER) && value <= BigInt(Number.MAX_SAFE_INTEGER)
    ? Number(value)
    : value.toString()
}

/**
 * Show decoded provider content beside LeapMux metadata and supplemental content.
 * Preserve numeric literals and repeated keys in both stored JSON sources.
 * The optional geometry field shows the measured row height.
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
  const completion = messageCompletionFromProto(message.completion)
  if (completion)
    envelope.completion = completion
  if (parsed.contentDecodeFailed)
    envelope.content_decode_failed = true
  if (message.supplementalRevision > 0n)
    envelope.supplemental_revision = jsonInt64(message.supplementalRevision)
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
  if (heights?.measured !== undefined) {
    envelope.geometry = { height: heights.measured }
  }

  const fields = Object.entries(envelope).flatMap(([key, value]) => {
    const encoded = JSON.stringify(value)
    return encoded === undefined ? [] : [`${JSON.stringify(key)}:${encoded}`]
  })
  const content = parsed.contentDecodeFailed
    ? JSON.stringify({ compression: message.contentCompression, base64: uint8ArrayToBase64(message.content) })
    : rawJsonValue(parsed.rawText)
  fields.push(`"content":${content}`)
  if (message.supplementalContent?.length) {
    const supplemental = parsed.supplementalRawText !== undefined
      ? rawJsonValue(parsed.supplementalRawText)
      : JSON.stringify(parsed.supplementalContent !== undefined ? parsed.supplementalContent : { compression: message.supplementalContentCompression, base64: uint8ArrayToBase64(message.supplementalContent) })
    fields.push(`"supplemental_content":${supplemental}`)
  }
  return `{${fields.join(',')}}`
}

/** Validate before embedding a JSON value. Invalid content remains a quoted string. */
function rawJsonValue(text: string): string {
  try {
    JSON.parse(text)
    return text
  }
  catch {
    return JSON.stringify(text)
  }
}
