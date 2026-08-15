/**
 * Byte-array helpers.
 *
 * These carry no protocol knowledge. They live here rather than in
 * `./noise` — where `concatBytes` was first defined — because the consumers
 * now span the Noise handshake, the key-pin store, and the terminal's input
 * queue, and a module named after a crypto protocol is the wrong place for
 * any of them to reach.
 */

/**
 * Join byte arrays into one, in order. Returns an empty array when given none.
 *
 * The result always owns a fresh buffer, so a caller may keep it after the
 * inputs are reused or cleared.
 */
export function concatBytes(...arrays: Uint8Array[]): Uint8Array {
  let totalLen = 0
  for (const a of arrays) totalLen += a.length
  const result = new Uint8Array(totalLen)
  let offset = 0
  for (const a of arrays) {
    result.set(a, offset)
    offset += a.length
  }
  return result
}
