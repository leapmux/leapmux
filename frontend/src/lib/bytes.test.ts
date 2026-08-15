import { describe, expect, it } from 'vitest'
import { concatBytes } from './bytes'

describe('concatBytes', () => {
  it('joins arrays in order', () => {
    expect([...concatBytes(new Uint8Array([1, 2]), new Uint8Array([3]))]).toEqual([1, 2, 3])
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
