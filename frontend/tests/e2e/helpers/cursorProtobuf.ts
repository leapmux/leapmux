/**
 * The schema-free protobuf primitives of the Cursor mock: the encoders, the one
 * reader, and the getters on what it reads. `./cursorWire` holds the message
 * layer that states Cursor's field numbers on top of these.
 *
 * ONE OCCURRENCE RULE. A singular field that appears more than once reads as its
 * LAST occurrence, as a proto3 parser reads a scalar. Every getter here and
 * `descend` follow that rule. A repeated field keeps every occurrence, in order.
 */

/** Protobuf wire type 0: a varint. */
const WIRE_VARINT = 0
/** Protobuf wire type 2: a length-delimited field. */
const WIRE_LENGTH_DELIMITED = 2
/** The largest field number that protobuf allows. */
const MAX_FIELD_NUMBER = 0x1FFF_FFFF

/** A base-128 varint: seven bits per byte, high bit set on every byte but the last. */
export function encodeVarint(value: number): Uint8Array {
  if (!Number.isSafeInteger(value) || value < 0)
    throw new Error(`A varint takes a non-negative integer in the safe integer range, not ${value}`)
  const bytes: number[] = []
  let rest = value
  do {
    const byte = rest & 0x7F
    rest = Math.floor(rest / 128)
    bytes.push(rest > 0 ? byte | 0x80 : byte)
  } while (rest > 0)
  return Uint8Array.from(bytes)
}

/** The tag of one field: its number and its wire type, as one varint. */
function encodeTag(fieldNumber: number, wireType: number): Uint8Array {
  if (!Number.isInteger(fieldNumber) || fieldNumber <= 0 || fieldNumber > MAX_FIELD_NUMBER)
    throw new Error('A protobuf field number must be an integer from 1 through 536870911.')
  // Multiply, not shift: a shift by three overflows the signed 32-bit range above field 2^28.
  return encodeVarint(fieldNumber * 8 + wireType)
}

/** Join byte arrays in order into one new array. */
export function concatBytes(parts: readonly Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((total, part) => total + part.byteLength, 0))
  let at = 0
  for (const part of parts) {
    out.set(part, at)
    at += part.byteLength
  }
  return out
}

/** One length-delimited field: its tag, its length, then the payload. */
export function encodeLengthDelimited(fieldNumber: number, payload: Uint8Array): Uint8Array {
  return concatBytes([encodeTag(fieldNumber, WIRE_LENGTH_DELIMITED), encodeVarint(payload.byteLength), payload])
}

/** One length-delimited field carrying UTF-8 text. */
export function encodeStringField(fieldNumber: number, value: string): Uint8Array {
  return encodeLengthDelimited(fieldNumber, new TextEncoder().encode(value))
}

/** One varint field: its tag, then its value. */
export function encodeVarintField(fieldNumber: number, value: number): Uint8Array {
  return concatBytes([encodeTag(fieldNumber, WIRE_VARINT), encodeVarint(value)])
}

/** One boolean field. Protobuf omits a false, so this writes the field only for a true. */
export function encodeBoolField(fieldNumber: number, value: boolean): Uint8Array {
  return value ? encodeVarintField(fieldNumber, 1) : new Uint8Array(0)
}

export interface CursorProtobufFields {
  /** Every length-delimited occurrence, by field number, in order. */
  strings: Map<number, Uint8Array[]>
  /** The last varint of each field. */
  integers: Map<number, bigint>
}

export interface CursorProtobufReadOptions {
  /**
   * Stop at the first malformed byte and keep the fields read before it, instead
   * of throwing. A read that searches an arbitrary client frame for one path uses
   * this: a heartbeat or an unrelated frame must not fail the stream.
   */
  lenient?: boolean
}

/** The failure of a malformed message, which a lenient read turns into the end of the walk. */
class MalformedCursorProtobuf extends Error {}

/**
 * Read every field of one message.
 *
 * A strict read refuses malformed bytes before a decoder trusts a result. A
 * lenient read keeps the fields that came before the malformed bytes.
 */
export function readCursorProtobufFields(bytes: Uint8Array, options: CursorProtobufReadOptions = {}): CursorProtobufFields {
  const fields: CursorProtobufFields = { strings: new Map(), integers: new Map() }
  try {
    readFieldsInto(bytes, fields)
  }
  catch (error) {
    if (!options.lenient || !(error instanceof MalformedCursorProtobuf))
      throw error
  }
  return fields
}

function readFieldsInto(bytes: Uint8Array, fields: CursorProtobufFields): void {
  let at = 0
  const take = (): bigint => {
    let value = 0n
    for (let index = 0; index < 10; index++) {
      const byte = bytes[at++]
      if (byte === undefined || (index === 9 && byte > 1))
        throw new MalformedCursorProtobuf('The native Cursor protobuf message has a truncated or invalid varint.')
      value |= BigInt(byte & 0x7F) << BigInt(index * 7)
      if ((byte & 0x80) === 0)
        return value
    }
    throw new MalformedCursorProtobuf('The native Cursor protobuf message has an invalid varint.')
  }
  while (at < bytes.length) {
    const tag = take()
    const field = Number(tag >> 3n)
    if (!Number.isSafeInteger(field) || field <= 0 || field > MAX_FIELD_NUMBER)
      throw new MalformedCursorProtobuf('The native Cursor protobuf message has an invalid field number.')
    switch (Number(tag & 7n)) {
      case WIRE_VARINT:
        fields.integers.set(field, take())
        break
      // A 64-bit fixed-width field, which no decoder here reads.
      case 1:
        at += 8
        break
      case WIRE_LENGTH_DELIMITED: {
        const length = take()
        if (length > BigInt(bytes.length - at))
          throw new MalformedCursorProtobuf('The native Cursor protobuf message has a truncated payload.')
        const size = Number(length)
        const values = fields.strings.get(field) ?? []
        values.push(bytes.subarray(at, at + size))
        fields.strings.set(field, values)
        at += size
        break
      }
      // A 32-bit fixed-width field, which no decoder here reads.
      case 5:
        at += 4
        break
      // A group, or a wire type that protobuf does not define. Neither appears in
      // Cursor's messages, and a read past it would misalign every later field.
      default:
        throw new MalformedCursorProtobuf('The native Cursor protobuf message has an unsupported wire type.')
    }
    if (at > bytes.length)
      throw new MalformedCursorProtobuf('The native Cursor protobuf message has a truncated fixed-width field.')
  }
}

/** The last occurrence of a length-delimited field, as its raw bytes. */
export function cursorProtobufBytes(fields: CursorProtobufFields, field: number): Uint8Array | undefined {
  return fields.strings.get(field)?.at(-1)
}

/** Every occurrence of a repeated length-delimited field, in order. */
export function cursorProtobufRepeated(fields: CursorProtobufFields, field: number): Uint8Array[] {
  return fields.strings.get(field) ?? []
}

/** The last occurrence of a length-delimited field, as UTF-8 text. */
export function cursorProtobufString(fields: CursorProtobufFields, field: number): string | undefined {
  const value = cursorProtobufBytes(fields, field)
  return value !== undefined ? new TextDecoder().decode(value) : undefined
}

/** A varint field as a number, or undefined when it is absent or outside the safe integer range. */
export function cursorProtobufNumber(fields: CursorProtobufFields, field: number, signed = false): number | undefined {
  const value = fields.integers.get(field)
  if (value === undefined)
    return undefined
  const normalized = signed ? BigInt.asIntN(32, value) : value
  return normalized <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(normalized) : undefined
}

/**
 * Follow a chain of length-delimited field numbers through nested messages.
 *
 * Each step reads leniently and takes the last occurrence, so a path through an
 * unrelated or malformed frame answers undefined rather than throwing.
 */
export function descend(bytes: Uint8Array, path: readonly number[]): Uint8Array | undefined {
  let current: Uint8Array | undefined = bytes
  for (const fieldNumber of path) {
    if (!current)
      return undefined
    current = cursorProtobufBytes(readCursorProtobufFields(current, { lenient: true }), fieldNumber)
  }
  return current
}
