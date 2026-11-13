import { blake2b } from '@noble/hashes/blake2.js'
import { bytesToHex } from '@noble/hashes/utils.js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { concatBytes } from './bytes'
import { digestFields, digestFieldsInSlices, FIELD_DIGEST_PIECE_BYTES } from './fieldDigest'

/**
 * Whether every check of the work slice reports it used, and how often the hash
 * yielded. While `alwaysUsed` is false, no slice is ever used up, whatever the
 * clock reads: a pause on a loaded runner must not add a yield. `gate` holds each
 * yield until it settles.
 */
const slicing = vi.hoisted(() => ({
  alwaysUsed: false,
  yields: 0,
  gate: Promise.resolve(),
}))

vi.mock('./workSlices', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workSlices')>()
  return {
    ...actual,
    createWorkSlicer: () => ({
      sliceUsed: () => slicing.alwaysUsed,
      nextSlice: async () => {
        slicing.yields++
        await slicing.gate
        await actual.yieldToEventLoop()
      },
    }),
  }
})

afterEach(() => {
  slicing.alwaysUsed = false
  slicing.yields = 0
  slicing.gate = Promise.resolve()
})

const encoder = new TextEncoder()

/** `length` bytes of a pattern that is not periodic in 64 KiB. */
function patterned(length: number): Uint8Array {
  return Uint8Array.from({ length }, (_, i) => i % 251)
}

/** The length of `field` as 8 bytes little-endian. */
function le64(field: Uint8Array): Uint8Array {
  const size = new Uint8Array(8)
  new DataView(size.buffer).setBigUint64(0, BigInt(field.byteLength), true)
  return size
}

/** The digest built by hand: one BLAKE2b-256 over `le64(len) || bytes` for each field. */
function referenceDigest(fields: Uint8Array[]): string {
  return bytesToHex(blake2b(concatBytes(...fields.flatMap(field => [
    le64(field),
    field,
  ])), { dkLen: 32 }))
}

/** The number of yields that `digestFieldsInSlices` takes for `fields` when every slice is used. */
async function slicedYields(fields: Uint8Array[]): Promise<number> {
  slicing.alwaysUsed = true
  slicing.yields = 0
  await digestFieldsInSlices(fields)
  slicing.alwaysUsed = false
  return slicing.yields
}

describe('digestFields', () => {
  it('matches a digest built by hand over the length-prefixed fields', () => {
    const fields = [
      encoder.encode('agent-1'),
      Uint8Array.of(1),
      patterned(FIELD_DIGEST_PIECE_BYTES + 3),
    ]
    expect(digestFields(fields)).toBe(referenceDigest(fields))
  })

  it('gives different digests for fields that concatenate to the same bytes', () => {
    expect(digestFields([
      encoder.encode('ab'),
      encoder.encode('c'),
    ]))
      .not
      .toBe(digestFields([
        encoder.encode('a'),
        encoder.encode('bc'),
      ]))
  })

  it('gives a stable digest for an empty field list and for one empty field, and the two differ', () => {
    const none = digestFields([])
    const oneEmpty = digestFields([
      new Uint8Array(0),
    ])

    expect(none).toBe(referenceDigest([]))
    expect(oneEmpty).toBe(referenceDigest([
      new Uint8Array(0),
    ]))
    expect(digestFields([])).toBe(none)
    expect(oneEmpty).not.toBe(none)
  })

  it('hashes the bytes of a subarray, not the rest of its buffer', () => {
    const whole = patterned(16)
    expect(digestFields([
      whole.subarray(4, 8),
    ])).toBe(referenceDigest([
      whole.slice(4, 8),
    ]))
  })
})

describe('digestFieldsInSlices', () => {
  it('equals digestFields for a field of more than eight pieces', async () => {
    const fields = [
      encoder.encode('large'),
      patterned(FIELD_DIGEST_PIECE_BYTES * 8 + 1),
    ]
    slicing.alwaysUsed = true

    expect(await digestFieldsInSlices(fields)).toBe(digestFields(fields))
  })

  it('yields to the event loop at least once per piece when every slice is used', async () => {
    const fields = [
      patterned(512 * 1024 + 1),
    ]
    slicing.alwaysUsed = true
    let hashed = false

    const digest = digestFieldsInSlices(fields).then((value) => {
      hashed = true
      return value
    })
    // Every microtask runs before the next task, so a hash that yields to the
    // event loop cannot finish during them.
    for (let hop = 0; hop < 100; hop++)
      await Promise.resolve()
    expect(hashed).toBe(false)

    expect(await digest).toBe(digestFields(fields))
    // 512 KiB + 1 byte is nine 64 KiB pieces, and each piece starts a new slice.
    expect(slicing.yields).toBeGreaterThanOrEqual(9)
  })

  it('does not yield for a short field list', async () => {
    const fields = [
      encoder.encode('agent-1'),
      encoder.encode('short'),
    ]

    expect(await digestFieldsInSlices(fields)).toBe(digestFields(fields))
    expect(slicing.yields).toBe(0)
  })

  // The length prefix is one update of its own, so a field of N pieces takes
  // N + 1 yields when every slice is used.
  it('splits a field of exactly one piece into one update, and one byte more into two', async () => {
    expect(await slicedYields([
      patterned(FIELD_DIGEST_PIECE_BYTES),
    ])).toBe(2)
    expect(await slicedYields([
      patterned(FIELD_DIGEST_PIECE_BYTES + 1),
    ])).toBe(3)
  })

  it('yields once for the length prefix alone of an empty field', async () => {
    expect(await slicedYields([
      new Uint8Array(0),
    ])).toBe(1)
    expect(await slicedYields([])).toBe(0)
  })
})
