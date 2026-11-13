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

/**
 * Return a view that covers the whole of its own `ArrayBuffer`, so that a
 * structured clone or a transfer moves exactly the bytes of `view`.
 *
 * A clone or a transfer moves the whole buffer behind a view, not the bytes that
 * the view covers. A subarray of a large buffer would carry the rest of that
 * buffer, and a transfer would detach it from every other view on it.
 *
 * Returns `view` itself when it already covers the whole of an `ArrayBuffer`.
 * Otherwise it returns a copy, which shares its buffer with no other view. A view
 * on a `SharedArrayBuffer` is always copied, because a transfer cannot move one.
 */
export function ownedBytes(view: Uint8Array): Uint8Array<ArrayBuffer> {
  const buffer = view.buffer
  if (buffer instanceof ArrayBuffer && view.byteOffset === 0 && view.byteLength === buffer.byteLength)
    return view as Uint8Array<ArrayBuffer>
  return view.slice()
}
