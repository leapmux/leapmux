import { describe, expect, it } from 'vitest'
import { frameBytes, unframeBytes } from './channelFraming'

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
    expect(result).toEqual({ ok: false, failure: { kind: 'short', length: 3 } })
  })

  it('rejects a length-mismatched frame', () => {
    const framed = frameBytes(new Uint8Array([1, 2]))
    // Corrupt the declared length.
    new DataView(framed.buffer).setUint32(0, 99)
    const result = unframeBytes(framed)
    expect(result).toEqual({ ok: false, failure: { kind: 'mismatch', declared: 99, actual: 2 } })
  })

  it('accepts an empty payload', () => {
    const framed = frameBytes(new Uint8Array(0))
    const result = unframeBytes(framed)
    expect(result.ok).toBe(true)
    if (result.ok)
      expect(result.payload.length).toBe(0)
  })
})
