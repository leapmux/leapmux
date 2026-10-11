import { describe, expect, it } from 'vitest'
import {
  concatBytes,
  cursorProtobufBytes,
  cursorProtobufNumber,
  cursorProtobufRepeated,
  cursorProtobufString,
  descend,
  encodeBoolField,
  encodeLengthDelimited,
  encodeStringField,
  encodeVarint,
  encodeVarintField,
  readCursorProtobufFields,
} from './cursorProtobuf'

const text = (bytes: Uint8Array | undefined) => bytes === undefined ? undefined : new TextDecoder().decode(bytes)

describe('encodeVarint', () => {
  it('rejects a number that cannot preserve its integer value', () => {
    expect(() => encodeVarint(Number.MAX_SAFE_INTEGER + 1)).toThrow('safe integer')
  })

  it('writes one byte below 128 and two above it', () => {
    expect([...encodeVarint(0)]).toEqual([0])
    expect([...encodeVarint(127)]).toEqual([0x7F])
    expect([...encodeVarint(128)]).toEqual([0x80, 0x01])
    expect([...encodeVarint(300)]).toEqual([0xAC, 0x02])
  })

  it('refuses a negative or fractional value rather than writing a wrong length', () => {
    expect(() => encodeVarint(-1)).toThrow('non-negative integer')
    expect(() => encodeVarint(1.5)).toThrow('non-negative integer')
  })
})

describe('encodeLengthDelimited', () => {
  it('encodes the largest valid field number without a signed bitwise overflow', () => {
    const field = 0x1FFF_FFFF
    expect(readCursorProtobufFields(encodeLengthDelimited(field, Uint8Array.from([42]))).strings.get(field)).toEqual([Uint8Array.from([42])])
  })

  it.each([0, -1, 1.5, 0x2000_0000, Number.POSITIVE_INFINITY])('rejects the invalid field number %s before encoding it', (field) => {
    expect(() => encodeLengthDelimited(field, new Uint8Array())).toThrow('field number')
  })

  it('writes the tag, the length, and the payload', () => {
    // Write tag 0x0A for field 1 with wire type 2.
    // Then write the length and payload bytes.
    expect([...encodeStringField(1, 'hi')]).toEqual([0x0A, 0x02, 0x68, 0x69])
  })

  it('writes a multi-byte length for a payload past 127 bytes', () => {
    const encoded = encodeLengthDelimited(1, new Uint8Array(200))
    expect([...encoded.subarray(0, 3)]).toEqual([0x0A, 0xC8, 0x01])
    expect(encoded.byteLength).toBe(203)
  })
})

describe('encodeVarintField', () => {
  it('writes the varint tag and the value, and keeps an explicit zero', () => {
    expect([...encodeVarintField(1, 0)]).toEqual([0x08, 0x00])
    expect([...encodeVarintField(3, 300)]).toEqual([0x18, 0xAC, 0x02])
  })

  it('encodes the largest valid field number without a signed bitwise overflow', () => {
    const field = 0x1FFF_FFFF
    expect(cursorProtobufNumber(readCursorProtobufFields(encodeVarintField(field, 7)), field)).toBe(7)
  })

  it.each([0, -1, 1.5, 0x2000_0000])('rejects the invalid field number %s', (field) => {
    expect(() => encodeVarintField(field, 1)).toThrow('field number')
  })

  it('rejects a negative value rather than writing a wrong varint', () => {
    expect(() => encodeVarintField(1, -1)).toThrow('non-negative integer')
  })
})

describe('encodeBoolField', () => {
  it('writes a true as one and omits a false, as protobuf does', () => {
    expect([...encodeBoolField(4, true)]).toEqual([0x20, 0x01])
    expect(encodeBoolField(4, false).byteLength).toBe(0)
  })
})

describe('concatBytes', () => {
  it('joins the parts in order, including empty ones', () => {
    expect([...concatBytes([Uint8Array.from([1]), new Uint8Array(0), Uint8Array.from([2, 3])])]).toEqual([1, 2, 3])
    expect(concatBytes([]).byteLength).toBe(0)
  })
})

describe('readCursorProtobufFields', () => {
  it('keeps repeated strings in order and reads the final value of a singular field', () => {
    const fields = readCursorProtobufFields(concatBytes([encodeStringField(1, 'first'), encodeStringField(1, 'last')]))
    expect(cursorProtobufRepeated(fields, 1).map(text)).toEqual(['first', 'last'])
    expect(cursorProtobufString(fields, 1)).toBe('last')
    expect(text(cursorProtobufBytes(fields, 1))).toBe('last')
    expect(cursorProtobufString(fields, 2)).toBeUndefined()
    expect(cursorProtobufBytes(fields, 2)).toBeUndefined()
    expect(cursorProtobufRepeated(fields, 2)).toEqual([])
  })

  it('reads the final value of a repeated singular varint', () => {
    expect(cursorProtobufNumber(readCursorProtobufFields(concatBytes([encodeVarintField(1, 5), encodeVarintField(1, 9)])), 1)).toBe(9)
  })

  it('keeps an empty string and an explicit zero distinct from absent fields', () => {
    const fields = readCursorProtobufFields(concatBytes([encodeStringField(1, ''), encodeVarintField(2, 0)]))
    expect(cursorProtobufString(fields, 1)).toBe('')
    expect(cursorProtobufNumber(fields, 2)).toBe(0)
    expect(cursorProtobufNumber(fields, 3)).toBeUndefined()
    expect(readCursorProtobufFields(new Uint8Array()).strings.size).toBe(0)
  })

  it('reads a signed int32 and refuses an unsafe unsigned number', () => {
    const fields = readCursorProtobufFields(Uint8Array.from([0x08, ...Array.from({ length: 9 }).fill(0xFF), 0x01]))
    expect(cursorProtobufNumber(fields, 1, true)).toBe(-1)
    expect(cursorProtobufNumber(fields, 1)).toBeUndefined()
  })

  // The reader must skip fields that it does not decode.
  // A neighboring fixed-width field must not shift later fields.
  it('skips a 64-bit and a 32-bit field without losing its place', () => {
    const fixed64Field = Uint8Array.from([(4 << 3) | 1, 1, 2, 3, 4, 5, 6, 7, 8])
    const fixed32Field = Uint8Array.from([(5 << 3) | 5, 1, 2, 3, 4])
    const fields = readCursorProtobufFields(concatBytes([encodeVarintField(2, 300), fixed64Field, encodeStringField(9, 'kept'), fixed32Field]))
    expect(cursorProtobufString(fields, 9)).toBe('kept')
    expect(cursorProtobufNumber(fields, 2)).toBe(300)
  })

  it.each([
    { bytes: [0], reason: 'invalid field number' },
    { bytes: [0x80], reason: 'truncated or invalid varint' },
    { bytes: [0x08, 0x80], reason: 'truncated or invalid varint' },
    { bytes: [0x0A, 4, 1], reason: 'truncated payload' },
    { bytes: [0x09, 1], reason: 'truncated fixed-width' },
    { bytes: [0x0D, 1], reason: 'truncated fixed-width' },
    { bytes: [0x0B], reason: 'unsupported wire type' },
  ])('rejects $reason in a strict read before decoding a native result', ({ bytes, reason }) => {
    expect(() => readCursorProtobufFields(Uint8Array.from(bytes))).toThrow(reason)
  })

  it.each([
    { label: 'a truncated varint', tail: [0x10, 0x80] },
    { label: 'a truncated payload', tail: [0x12, 4, 1] },
    { label: 'an unsupported wire type', tail: [0x0B, 0x0A, 1, 0x61] },
    { label: 'a field number of zero', tail: [0, 0x0A, 1, 0x61] },
  ])('keeps the fields before $label in a lenient read, and drops the rest', ({ tail }) => {
    const bytes = concatBytes([encodeStringField(1, 'kept'), Uint8Array.from(tail)])
    const fields = readCursorProtobufFields(bytes, { lenient: true })
    expect(cursorProtobufRepeated(fields, 1).map(text)).toEqual(['kept'])
    expect(fields.strings.has(2)).toBe(false)
    expect(fields.integers.size).toBe(0)
  })

  it('reads a well-formed message the same way in both modes', () => {
    const bytes = concatBytes([encodeStringField(1, 'a'), encodeVarintField(2, 7), encodeStringField(1, 'b')])
    expect(readCursorProtobufFields(bytes, { lenient: true })).toEqual(readCursorProtobufFields(bytes))
  })
})

describe('descend', () => {
  it('follows a chain of nested fields', () => {
    const nested = encodeLengthDelimited(1, encodeLengthDelimited(2, encodeStringField(3, 'deep')))
    expect(text(descend(nested, [1, 2, 3]))).toBe('deep')
  })

  it('answers undefined for a path that leaves the message', () => {
    expect(descend(encodeStringField(1, 'x'), [1, 2])).toBeUndefined()
    expect(descend(encodeStringField(1, 'x'), [4])).toBeUndefined()
  })

  it('takes the last occurrence of each field on the path', () => {
    const message = concatBytes([encodeLengthDelimited(1, encodeStringField(2, 'first')), encodeLengthDelimited(1, encodeStringField(2, 'last'))])
    expect(text(descend(message, [1, 2]))).toBe('last')
  })

  it('answers undefined, rather than throwing, for a path through malformed bytes', () => {
    expect(descend(Uint8Array.from([0x0A, 4, 1]), [1])).toBeUndefined()
    expect(text(descend(concatBytes([encodeStringField(1, 'kept'), Uint8Array.from([0x80])]), [1]))).toBe('kept')
  })
})
