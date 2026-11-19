import { describe, expect, it } from 'vitest'
import { frameAssociatedData, frameBytes, unframeBytes } from './channelFraming'

describe('channelFraming', () => {
  it('round-trips a payload through frameBytes and unframeBytes', () => {
    const payload = new Uint8Array([1, 2, 3, 4, 5])
    const framed = frameBytes(payload)
    expect(framed.length).toBe(9)
    const result = unframeBytes(framed)
    expect(result.ok).toBe(true)
    if (result.ok)
      expect([...result.payload]).toEqual([1, 2, 3, 4, 5])
  })

  it('rejects a buffer shorter than the length prefix', () => {
    const result = unframeBytes(new Uint8Array([0, 0, 1]))
    expect(result).toEqual({
      ok: false,
      failure: { kind: 'short', length: 3 },
    })
  })

  it('rejects a length-mismatched frame', () => {
    const framed = frameBytes(new Uint8Array([1, 2]))
    // Corrupt the declared length.
    new DataView(framed.buffer).setUint32(0, 99)
    const result = unframeBytes(framed)
    expect(result).toEqual({
      ok: false,
      failure: { kind: 'mismatch', declared: 99, actual: 2 },
    })
  })

  it('accepts an empty payload', () => {
    const framed = frameBytes(new Uint8Array(0))
    const result = unframeBytes(framed)
    expect(result.ok).toBe(true)
    if (result.ok)
      expect(result.payload.length).toBe(0)
  })
})

describe('frameAssociatedData', () => {
  it.each([
    {
      name: 'channel and MORE',
      channelId: 'abc',
      correlationId: 5n,
      flags: 1,
      want: '000000000000000500000001616263',
    },
    {
      name: 'largest uint64',
      channelId: 'abc',
      correlationId: 0xFFFFFFFFFFFFFFFFn,
      flags: 0,
      want: 'ffffffffffffffff00000000616263',
    },
    {
      name: 'out-of-spec flags',
      channelId: '',
      correlationId: 5n,
      flags: 3,
      want: '000000000000000500000003',
    },
    {
      name: 'empty channel id',
      channelId: '',
      correlationId: 0n,
      flags: 0,
      want: '000000000000000000000000',
    },
  ])('encodes $name', ({ channelId, correlationId, flags, want }) => {
    const expected = Uint8Array.from(want.match(/../g)!.map(byte => Number.parseInt(byte, 16)))
    expect(frameAssociatedData(channelId, correlationId, flags)).toEqual(expected)
  })
})
