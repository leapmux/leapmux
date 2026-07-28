/**
 * Shared length-prefix framing for multiplexed channel WebSocket frames
 * (and the same BE-uint32 layout used by userevents).
 *
 * Wire format: [4 bytes big-endian length][payload].
 * Encode lives next to decode so a framing change cannot desync send vs receive.
 */

export const LENGTH_PREFIX_BYTES = 4

/** Prefix `payload` with its big-endian length. */
export function frameBytes(payload: Uint8Array): Uint8Array {
  const buf = new Uint8Array(LENGTH_PREFIX_BYTES + payload.length)
  new DataView(buf.buffer).setUint32(0, payload.length)
  buf.set(payload, LENGTH_PREFIX_BYTES)
  return buf
}

export type UnframeFailure
  = | { kind: 'short', length: number }
    | { kind: 'mismatch', declared: number, actual: number }

/**
 * Strip a length prefix. Returns the payload view (zero-copy subarray) or a
 * structured failure — callers log; never throw on a framing violation.
 */
export function unframeBytes(buf: Uint8Array): { ok: true, payload: Uint8Array } | { ok: false, failure: UnframeFailure } {
  if (buf.length < LENGTH_PREFIX_BYTES) {
    return { ok: false, failure: { kind: 'short', length: buf.length } }
  }
  const declared = new DataView(buf.buffer, buf.byteOffset).getUint32(0)
  const actual = buf.length - LENGTH_PREFIX_BYTES
  if (declared !== actual) {
    return { ok: false, failure: { kind: 'mismatch', declared, actual } }
  }
  return { ok: true, payload: buf.subarray(LENGTH_PREFIX_BYTES) }
}
