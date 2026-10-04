import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { cursorProtobufNumber, cursorProtobufString, readCursorProtobufFields } from './cursorProtobuf'
import { encodeStringField } from './cursorWire'

describe('readCursorProtobufFields', () => {
  it('keeps repeated strings and reads the final value', () => {
    const fields = readCursorProtobufFields(Buffer.concat([encodeStringField(1, 'first'), encodeStringField(1, 'last')]))
    expect(fields.strings.get(1)).toHaveLength(2)
    expect(cursorProtobufString(fields, 1)).toBe('last')
    expect(cursorProtobufString(fields, 2)).toBeUndefined()
  })

  it('keeps an empty string and an explicit zero distinct from absent fields', () => {
    const fields = readCursorProtobufFields(Buffer.concat([encodeStringField(1, ''), Uint8Array.from([0x10, 0])]))
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

  it.each([
    { bytes: [0], reason: 'invalid field number' },
    { bytes: [0x80], reason: 'truncated or invalid varint' },
    { bytes: [0x0A, 4, 1], reason: 'truncated payload' },
    { bytes: [0x09, 1], reason: 'truncated fixed-width' },
    { bytes: [0x0D, 1], reason: 'truncated fixed-width' },
    { bytes: [0x0B], reason: 'unsupported wire type' },
  ])('rejects $reason before decoding a native result', ({ bytes, reason }) => {
    expect(() => readCursorProtobufFields(Uint8Array.from(bytes))).toThrow(reason)
  })
})
