/**
 * The AWS event stream: the binary framing that an AWS streaming operation answers
 * with, `application/vnd.amazon.eventstream`.
 *
 * One message is a length prelude, a list of typed headers, a payload, and two
 * CRC32 checksums. Every integer is big-endian:
 *
 * | Offset | Size | Field |
 * |---|---|---|
 * | 0 | 4 | total length = 12 + headers length + payload length + 4 |
 * | 4 | 4 | headers length |
 * | 8 | 4 | prelude CRC32 of bytes 0 to 7 |
 * | 12 | H | headers |
 * | 12 + H | P | payload |
 * | end - 4 | 4 | message CRC32 of every byte before it |
 *
 * One header is the name length (1 byte), the ASCII name, the value type (1 byte),
 * and the value. Only the string type (7) is written here: a value length of 2 bytes,
 * then the UTF-8 value. The mock needs no other type, and the decoder refuses one.
 *
 * Nothing here knows a service. The Kiro surface of the mock (`./kiroSurface`) is the
 * one caller today, and any other AWS streaming operation frames its events the same
 * way.
 */
import { Buffer } from 'node:buffer'
import { crc32 } from 'node:zlib'

/** The content type of an event stream. */
export const EVENT_STREAM_CONTENT_TYPE = 'application/vnd.amazon.eventstream'

/** The value type of a string header. */
const HEADER_TYPE_STRING = 7

/** The fixed bytes around the headers and the payload: the prelude, its CRC, and the message CRC. */
const PRELUDE_BYTES = 8
const PRELUDE_CRC_BYTES = 4
const MESSAGE_CRC_BYTES = 4

/** The largest name one header can state: its length is one byte. */
const MAX_HEADER_NAME_BYTES = 0xFF

/** The largest string one header can state: its length is two bytes. */
const MAX_HEADER_VALUE_BYTES = 0xFFFF

/** The bytes of one header around its name and its value: the name length, the type, and the value length. */
const HEADER_NAME_LENGTH_BYTES = 1
const HEADER_TYPE_BYTES = 1
const HEADER_VALUE_LENGTH_BYTES = 2

/** Every character of a printable ASCII string: a space (U+0020) to a tilde (U+007E). */
const PRINTABLE_ASCII = /^[\x20-\x7E]*$/

/** One decoded message: its string headers by name, and its payload. */
export interface EventStreamMessage {
  headers: Record<string, string>
  payload: Buffer
}

/**
 * Encode one message with string headers and a payload.
 *
 * The header order is the order of `headers`. The CRCs are IEEE CRC32, as `zlib`
 * computes them.
 */
export function encodeEventStreamMessage(headers: Readonly<Record<string, string>>, payload: Buffer): Buffer {
  const encodedHeaders = Buffer.concat(Object.entries(headers).map(([name, value]) => encodeStringHeader(name, value)))
  const totalLength = PRELUDE_BYTES + PRELUDE_CRC_BYTES + encodedHeaders.byteLength + payload.byteLength + MESSAGE_CRC_BYTES
  const prelude = Buffer.alloc(PRELUDE_BYTES)
  prelude.writeUInt32BE(totalLength, 0)
  prelude.writeUInt32BE(encodedHeaders.byteLength, 4)
  const preludeCrc = Buffer.alloc(PRELUDE_CRC_BYTES)
  preludeCrc.writeUInt32BE(crc32(prelude), 0)
  const body = Buffer.concat([prelude, preludeCrc, encodedHeaders, payload])
  const messageCrc = Buffer.alloc(MESSAGE_CRC_BYTES)
  messageCrc.writeUInt32BE(crc32(body), 0)
  return Buffer.concat([body, messageCrc])
}

/**
 * Encode one EVENT: the three headers every event carries, and a JSON payload.
 *
 * `:event-type` identifies the event, and the client dispatches on it.
 */
export function encodeEventStreamEvent(eventType: string, payload: unknown): Buffer {
  return encodeEventStreamMessage({
    ':event-type': eventType,
    ':content-type': 'application/json',
    ':message-type': 'event',
  }, Buffer.from(JSON.stringify(payload), 'utf8'))
}

/**
 * Encode one modeled EXCEPTION inside a stream: an error that arrives after the
 * response started. A client raises the error that `:exception-type` states.
 */
export function encodeEventStreamException(exceptionType: string, payload: unknown): Buffer {
  return encodeEventStreamMessage({
    ':exception-type': exceptionType,
    ':content-type': 'application/json',
    ':message-type': 'exception',
  }, Buffer.from(JSON.stringify(payload), 'utf8'))
}

function encodeStringHeader(name: string, value: string): Buffer {
  // `Buffer.from(name, 'ascii')` writes a character above U+007F as its low byte,
  // so a client would read a different name. Refuse it instead, and refuse a control
  // character, which no header name holds.
  if (!PRINTABLE_ASCII.test(name))
    throw new Error(`An event stream header name must be printable ASCII: ${JSON.stringify(name)}`)
  const encodedName = Buffer.from(name, 'ascii')
  const encodedValue = Buffer.from(value, 'utf8')
  if (encodedName.byteLength === 0 || encodedName.byteLength > MAX_HEADER_NAME_BYTES)
    throw new Error(`An event stream header name must be 1 to ${MAX_HEADER_NAME_BYTES} bytes: ${JSON.stringify(name)}`)
  if (encodedValue.byteLength > MAX_HEADER_VALUE_BYTES)
    throw new Error(`The event stream header ${name} holds more than ${MAX_HEADER_VALUE_BYTES} bytes`)
  const lengths = Buffer.alloc(1)
  lengths.writeUInt8(encodedName.byteLength, 0)
  const typeAndLength = Buffer.alloc(3)
  typeAndLength.writeUInt8(HEADER_TYPE_STRING, 0)
  typeAndLength.writeUInt16BE(encodedValue.byteLength, 1)
  return Buffer.concat([lengths, encodedName, typeAndLength, encodedValue])
}

/**
 * Decode every whole message in a buffer, checking both CRCs.
 *
 * For a test that reads what the mock wrote. Each of these throws, because a decoder
 * that skipped it would let a broken encoder pass:
 *
 * - A buffer that ends inside a message.
 * - A bad CRC.
 * - A header of a type other than string.
 * - A header whose name, type or value runs past the header block.
 */
export function decodeEventStreamMessages(data: Buffer): EventStreamMessage[] {
  const messages: EventStreamMessage[] = []
  let offset = 0
  while (offset < data.byteLength) {
    if (data.byteLength - offset < PRELUDE_BYTES + PRELUDE_CRC_BYTES)
      throw new Error(`The event stream ends inside a prelude at byte ${offset}`)
    const totalLength = data.readUInt32BE(offset)
    const headersLength = data.readUInt32BE(offset + 4)
    if (crc32(data.subarray(offset, offset + PRELUDE_BYTES)) !== data.readUInt32BE(offset + PRELUDE_BYTES))
      throw new Error(`The prelude CRC of the message at byte ${offset} does not match`)
    if (totalLength < PRELUDE_BYTES + PRELUDE_CRC_BYTES + headersLength + MESSAGE_CRC_BYTES || offset + totalLength > data.byteLength)
      throw new Error(`The message at byte ${offset} states a length of ${totalLength} that the buffer cannot hold`)
    const end = offset + totalLength
    if (crc32(data.subarray(offset, end - MESSAGE_CRC_BYTES)) !== data.readUInt32BE(end - MESSAGE_CRC_BYTES))
      throw new Error(`The message CRC of the message at byte ${offset} does not match`)
    const headersStart = offset + PRELUDE_BYTES + PRELUDE_CRC_BYTES
    const payloadStart = headersStart + headersLength
    messages.push({
      headers: decodeStringHeaders(data.subarray(headersStart, payloadStart)),
      payload: Buffer.from(data.subarray(payloadStart, end - MESSAGE_CRC_BYTES)),
    })
    offset = end
  }
  return messages
}

function decodeStringHeaders(data: Buffer): Record<string, string> {
  const headers: Record<string, string> = {}
  let offset = 0
  // Each field of a header must end inside the block. A subarray past the end
  // returns fewer bytes and no error, so an encoder that wrote a short value would
  // otherwise pass.
  const requireInside = (end: number, name: string, field: string) => {
    if (end > data.byteLength)
      throw new Error(`The ${field} of the event stream header ${JSON.stringify(name)} runs past the header block of ${data.byteLength} bytes`)
  }
  while (offset < data.byteLength) {
    const nameLength = data.readUInt8(offset)
    const nameEnd = offset + HEADER_NAME_LENGTH_BYTES + nameLength
    requireInside(nameEnd, '', 'name')
    const name = data.subarray(offset + HEADER_NAME_LENGTH_BYTES, nameEnd).toString('ascii')
    requireInside(nameEnd + HEADER_TYPE_BYTES + HEADER_VALUE_LENGTH_BYTES, name, 'type and value length')
    const type = data.readUInt8(nameEnd)
    if (type !== HEADER_TYPE_STRING)
      throw new Error(`The event stream header ${name} has the value type ${type}, and only the string type decodes here`)
    const valueLength = data.readUInt16BE(nameEnd + HEADER_TYPE_BYTES)
    const valueStart = nameEnd + HEADER_TYPE_BYTES + HEADER_VALUE_LENGTH_BYTES
    requireInside(valueStart + valueLength, name, 'value')
    headers[name] = data.subarray(valueStart, valueStart + valueLength).toString('utf8')
    offset = valueStart + valueLength
  }
  return headers
}
