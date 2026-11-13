import { describe, expect, it } from 'vitest'
import { concatBytes, ownedBytes } from './bytes'

describe('concatBytes', () => {
  it('joins arrays in order', () => {
    expect([
      ...concatBytes(new Uint8Array([1, 2]), new Uint8Array([3])),
    ]).toEqual([1, 2, 3])
  })

  it('returns an empty array when given none', () => {
    expect(concatBytes()).toEqual(new Uint8Array(0))
  })

  it('skips empty inputs without disturbing the order', () => {
    const joined = concatBytes(
      new Uint8Array(0),
      new Uint8Array([1]),
      new Uint8Array(0),
      new Uint8Array([2, 3]),
    )
    expect([...joined]).toEqual([1, 2, 3])
  })

  it('copies, so the result survives a mutated input', () => {
    const source = new Uint8Array([1, 2])
    const joined = concatBytes(source)
    source[0] = 9
    expect([...joined]).toEqual([1, 2])
  })

  it('preserves multi-byte UTF-8 split across arrays', () => {
    // The terminal's input queue concatenates encoded keystrokes, so a syllable
    // must survive whatever boundary the batching happens to fall on.
    const encoded = new TextEncoder().encode('안녕')
    const joined = concatBytes(encoded.slice(0, 2), encoded.slice(2))
    expect(new TextDecoder().decode(joined)).toBe('안녕')
  })
})

describe('ownedBytes', () => {
  it('returns the same view for a view that covers its whole buffer', () => {
    const view = new Uint8Array([1, 2, 3])
    expect(ownedBytes(view)).toBe(view)
  })

  it('copies a subarray into a buffer of its own length', () => {
    const source = new Uint8Array([1, 2, 3, 4, 5])
    const view = source.subarray(1, 4)
    const owned = ownedBytes(view)

    expect(owned).not.toBe(view)
    expect(owned.buffer.byteLength).toBe(3)
    expect(owned.byteOffset).toBe(0)
    expect([...owned]).toEqual([2, 3, 4])
    owned[0] = 9
    expect([...source]).toEqual([1, 2, 3, 4, 5])
  })

  it('copies a view that starts at 0 but covers less than its buffer', () => {
    const buffer = new ArrayBuffer(8)
    const view = new Uint8Array(buffer, 0, 4)
    view.set([7, 8, 9, 10])
    const owned = ownedBytes(view)

    expect(owned).not.toBe(view)
    expect(owned.buffer).not.toBe(buffer)
    expect(owned.buffer.byteLength).toBe(4)
    expect([...owned]).toEqual([7, 8, 9, 10])
  })

  it('returns an empty view that covers its whole buffer for an empty subarray', () => {
    const view = new Uint8Array([1, 2, 3]).subarray(3)
    const owned = ownedBytes(view)

    expect(owned.byteLength).toBe(0)
    expect(owned.byteOffset).toBe(0)
    expect(owned.buffer.byteLength).toBe(0)
  })

  it('returns an empty view itself when it covers its whole empty buffer', () => {
    const view = new Uint8Array(0)
    expect(ownedBytes(view)).toBe(view)
  })

  it('copies a view on a SharedArrayBuffer into a plain ArrayBuffer', () => {
    const view = new Uint8Array(new SharedArrayBuffer(2))
    view.set([4, 5])
    const owned = ownedBytes(view)

    expect(owned.buffer).toBeInstanceOf(ArrayBuffer)
    expect([...owned]).toEqual([4, 5])
  })
})
