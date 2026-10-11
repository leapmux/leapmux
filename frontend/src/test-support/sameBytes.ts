import { Buffer } from 'node:buffer'

/** A view of the memory of `bytes`, with no copy. */
function bufferOf(bytes: Uint8Array): Buffer {
  return Buffer.from(bytes.buffer, bytes.byteOffset, bytes.byteLength)
}

/**
 * Require that `actual` holds exactly the bytes of `expected`.
 *
 * `toEqual` compares a typed array one element at a time through its generic equality. For the half megabyte of a
 * stored native tool record, that takes most of a second alone and several seconds under load, which reaches the
 * 5-second limit of a test. `Buffer.equals` compares the memory at once.
 *
 * A failure states both lengths and the first index that differs, where `toEqual` prints a diff of every byte.
 * `label` states what the bytes are, for the failure message.
 */
export function expectSameBytes(actual: Uint8Array, expected: Uint8Array, label = 'the bytes'): void {
  if (bufferOf(actual).equals(bufferOf(expected)))
    return
  const shorter = Math.min(actual.length, expected.length)
  let index = 0
  while (index < shorter && actual[index] === expected[index])
    index++
  throw new Error(`${label} differ: ${actual.length} bytes against ${expected.length} expected, and the first difference is at index ${index}.`)
}
