import { describe, expect, it } from 'vitest'
import { literalCodeBlock } from './literalCodeBlock'

describe('literalCodeBlock', () => {
  it('preserves empty text and Unicode', () => {
    expect(literalCodeBlock('')).toBe('```\n\n```')
    expect(literalCodeBlock('日本語\n🙂')).toBe('```\n日本語\n🙂\n```')
  })

  it('keeps embedded fences inside the literal block', () => {
    expect(literalCodeBlock('a```b')).toBe('````\na```b\n````')
    const embedded = '`'.repeat(1000)
    const fence = '`'.repeat(1001)
    expect(literalCodeBlock(embedded)).toBe(`${fence}\n${embedded}\n${fence}`)
  })
})
