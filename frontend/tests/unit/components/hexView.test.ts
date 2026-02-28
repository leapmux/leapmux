import { describe, expect, it } from 'vitest'
import { formatRow } from '~/components/fileviewer/HexView'

describe('formatRow', () => {
  it('formats a full 16-byte row', () => {
    const bytes = new Uint8Array([
      0x48,
      0x65,
      0x6C,
      0x6C,
      0x6F,
      0x20,
      0x57,
      0x6F,
      0x72,
      0x6C,
      0x64,
      0x21,
      0x0A,
      0x00,
      0x00,
      0x00,
    ])
    const result = formatRow(bytes, 0)
    expect(result.offsetStr).toBe('00000000')
    expect(result.hex).toBe('48 65 6c 6c 6f 20 57 6f  72 6c 64 21 0a 00 00 00')
    expect(result.ascii).toBe('Hello World!....')
  })

  it('formats a partial row with padding', () => {
    const bytes = new Uint8Array([0x48, 0x69])
    const result = formatRow(bytes, 0x10)
    expect(result.offsetStr).toBe('00000010')
    expect(result.hex).toBe('48 69                                           ')
    expect(result.ascii).toBe('Hi              ')
  })

  it('replaces non-printable chars with dots in ASCII', () => {
    const bytes = new Uint8Array([0x01, 0x7F, 0x41, 0x1B])
    const result = formatRow(bytes, 0)
    // 0x01 -> '.', 0x7F -> '.', 0x41 -> 'A', 0x1B -> '.' (ESC is < 0x20)
    expect(result.ascii.substring(0, 4)).toBe('..A.')
  })

  it('formats offset correctly for large addresses', () => {
    const bytes = new Uint8Array([0x41])
    const result = formatRow(bytes, 0x1FFFFF0)
    expect(result.offsetStr).toBe('01fffff0')
  })

  it('handles empty bytes', () => {
    const bytes = new Uint8Array(0)
    const result = formatRow(bytes, 0)
    expect(result.offsetStr).toBe('00000000')
    expect(result.ascii).toBe('                ')
  })

  it('handles exactly 8 bytes (first group full, second group empty)', () => {
    const bytes = new Uint8Array([0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48])
    const result = formatRow(bytes, 0)
    expect(result.hex).toBe('41 42 43 44 45 46 47 48                         ')
    expect(result.ascii).toBe('ABCDEFGH        ')
  })
})
