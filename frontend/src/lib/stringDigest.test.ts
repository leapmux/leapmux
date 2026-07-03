import { describe, expect, it } from 'vitest'
import { fnv1a32Hex } from './stringDigest'

describe('fnv1a32hex', () => {
  it('matches the published FNV-1a 32-bit test vectors', () => {
    // Offset basis (empty input) and classic vectors from the FNV reference.
    expect(fnv1a32Hex('')).toBe('811c9dc5')
    expect(fnv1a32Hex('a')).toBe('e40c292c')
    expect(fnv1a32Hex('foobar')).toBe('bf9cf968')
  })

  it('is stable and input-sensitive', () => {
    const key = '42|s|1|0:|r|2|0:|0|3|c|["800",1,"unified","/w","/h"]'
    expect(fnv1a32Hex(key)).toBe(fnv1a32Hex(key))
    expect(fnv1a32Hex(key)).not.toBe(fnv1a32Hex(`${key} `))
    expect(fnv1a32Hex(key)).toHaveLength(8)
  })
})
