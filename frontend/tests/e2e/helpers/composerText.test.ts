import { describe, expect, it } from 'vitest'
import { paragraphKeys } from './composerText'

describe('paragraphKeys', () => {
  it('types one line break for each blank line between paragraphs', () => {
    expect(paragraphKeys('/compact\n\nLEAPMUXE2ESCENARIO:test-1')).toBe('/compact\nLEAPMUXE2ESCENARIO:test-1')
    expect(paragraphKeys('first\n\nsecond\n\nthird')).toBe('first\nsecond\nthird')
  })

  it('types a single paragraph as it is', () => {
    expect(paragraphKeys('/compact')).toBe('/compact')
  })

  it.each([
    ['an empty text', ''],
    ['a line break inside a paragraph', 'first\nsecond'],
    ['an empty paragraph between two blank lines', 'first\n\n\n\nsecond'],
    ['a leading blank line', '\n\nfirst'],
    ['a trailing blank line', 'first\n\n'],
  ])('refuses %s, which typed paragraphs cannot send', (_label, markdown) => {
    expect(() => paragraphKeys(markdown)).toThrow('The composer cannot send')
  })
})
