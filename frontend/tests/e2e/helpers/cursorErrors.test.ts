import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { cursorErrorResponse } from './cursorErrors'
import { descend, readLengthDelimitedFields } from './cursorWire'

interface ConnectErrorReply {
  error: { code: string, message: string, details: Array<{ type: string, value: string }> }
}

function decodeError(status: number, message: string): { reply: ConnectErrorReply, details: Uint8Array } {
  const { frame, error } = cursorErrorResponse({ status, message })
  expect(frame[0]).toBe(2)
  expect(new DataView(frame.buffer, frame.byteOffset).getUint32(1)).toBe(frame.length - 5)
  const reply = JSON.parse(new TextDecoder().decode(frame.subarray(5))) as ConnectErrorReply
  expect(error).toEqual({ code: reply.error.code, message: reply.error.message })
  expect(reply.error.details).toHaveLength(1)
  expect(reply.error.details[0]?.type).toBe('aiserver.v1.ErrorDetails')
  return { reply, details: Buffer.from(reply.error.details[0]!.value, 'base64') }
}

describe('cursorErrorResponse', () => {
  it('uses a native nonretryable quota error on the Connect end response', () => {
    const { reply, details } = decodeError(429, 'CURSOR_NATIVE_QUOTA')
    expect(reply.error).toMatchObject({ code: 'resource_exhausted', message: 'CURSOR_NATIVE_QUOTA' })
    expect([...details.subarray(0, 2)]).toEqual([0x08, 10])
    const custom = descend(details, [2])!
    const fields = readLengthDelimitedFields(custom)
    expect(new TextDecoder().decode(fields.get(1)?.[0])).toBe('Usage limit reached')
    expect(new TextDecoder().decode(fields.get(2)?.[0])).toBe('CURSOR_NATIVE_QUOTA')
    expect([...custom.subarray(-2)]).toEqual([0x20, 0])
    expect([...details.subarray(-2)]).toEqual([0x18, 1])
  })

  it.each([
    [400, 'invalid_argument'],
    [401, 'unauthenticated'],
    [403, 'permission_denied'],
    [404, 'not_found'],
    [408, 'deadline_exceeded'],
    [409, 'aborted'],
    [500, 'internal'],
    [501, 'unimplemented'],
    [502, 'unavailable'],
    [503, 'unavailable'],
    [504, 'deadline_exceeded'],
    [599, 'internal'],
  ])('maps HTTP %i to native Connect %s', (status, code) => {
    const { reply, details } = decodeError(status, 'Native model failure.')
    expect(reply.error.code).toBe(code)
    expect([...details.subarray(0, 2)]).toEqual([0x08, 29])
    expect([...descend(details, [2])!.subarray(-2)]).toEqual([0x20, 0])
  })

  it.each(['', 'Native quota: 零 🔒'])('preserves the exact UTF-8 message %s', (message) => {
    const { reply, details } = decodeError(429, message)
    expect(reply.error.message).toBe(message)
    expect(new TextDecoder().decode(descend(details, [2, 2]))).toBe(message)
  })

  it.each([0, -1, 399, 600, 429.5, Number.POSITIVE_INFINITY])('refuses an invalid HTTP error status %s', (status) => {
    expect(() => cursorErrorResponse({ status, message: 'Native error.' })).toThrow('HTTP error status')
  })
})
