import { blake2b } from '@noble/hashes/blake2.js'
import { bytesToHex } from '@noble/hashes/utils.js'
import { createWorkSlicer } from './workSlices'

// ---------------------------------------------------------------------------
// Field digest
//
// The digest of an ordered list of byte fields: BLAKE2b-256 over each field's
// length as 8 bytes little-endian, followed by the field itself. The length
// prefixes keep `['ab', 'c']` and `['a', 'bc']` apart.
//
// `digestFields` runs in one task, in the hash worker (see fieldDigestWorker).
// `digestFieldsInSlices` computes the same digest on the main thread in work
// slices, for a worker that cannot answer (see fieldDigestClient). BLAKE2b does
// not depend on where the update calls split its input, so the two agree.
// ---------------------------------------------------------------------------

/**
 * The most bytes that one `hash.update` takes. BLAKE2b in JavaScript hashes about
 * 60 MiB per second, so a piece takes about 1 ms, well inside a work slice.
 */
export const FIELD_DIGEST_PIECE_BYTES = 64 * 1024

/**
 * The input of the digest, in order: for each field, its length as 8 bytes
 * little-endian, then the field in pieces of at most FIELD_DIGEST_PIECE_BYTES.
 * An empty field yields its length prefix alone.
 */
function* digestInput(fields: readonly Uint8Array[]): Generator<Uint8Array> {
  for (const field of fields) {
    const size = new Uint8Array(8)
    new DataView(size.buffer).setBigUint64(0, BigInt(field.byteLength), true)
    yield size
    for (let offset = 0; offset < field.byteLength; offset += FIELD_DIGEST_PIECE_BYTES)
      yield field.subarray(offset, offset + FIELD_DIGEST_PIECE_BYTES)
  }
}

/** The hex digest of `fields`, computed in one task. */
export function digestFields(fields: readonly Uint8Array[]): string {
  const hash = blake2b.create({ dkLen: 32 })
  for (const piece of digestInput(fields))
    hash.update(piece)
  return bytesToHex(hash.digest())
}

/**
 * The same digest as `digestFields`, computed in work slices that yield to the
 * event loop, so that the page stays responsive while it hashes up to 10 MiB.
 */
export async function digestFieldsInSlices(fields: readonly Uint8Array[]): Promise<string> {
  const hash = blake2b.create({ dkLen: 32 })
  const slicer = createWorkSlicer()
  for (const piece of digestInput(fields)) {
    if (slicer.sliceUsed())
      await slicer.nextSlice()
    hash.update(piece)
  }
  return bytesToHex(hash.digest())
}
