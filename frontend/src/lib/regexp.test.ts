import { describe, expect, it } from 'vitest'
import { escapeRegExp } from './regexp'

const SYNTAX_CHARACTERS = ['.', '*', '+', '?', '^', '$', '{', '}', '(', ')', '|', '[', ']', '\\']

describe('escapeRegExp', () => {
  it.each(SYNTAX_CHARACTERS)('matches the syntax character %j literally', (character) => {
    const text = `a${character}b`
    for (const flags of ['', 'u']) {
      const pattern = new RegExp(`^${escapeRegExp(text)}$`, flags)
      expect(pattern.test(text)).toBe(true)
    }
  })

  it('matches a text that holds every syntax character, and nothing else', () => {
    const text = SYNTAX_CHARACTERS.join('')
    const pattern = new RegExp(`^${escapeRegExp(text)}$`, 'u')
    expect(pattern.test(text)).toBe(true)
    expect(pattern.test(`${text}x`)).toBe(false)
    expect(pattern.test(text.slice(1))).toBe(false)
  })

  it('does not let a dot match another character', () => {
    expect(new RegExp(`^${escapeRegExp('a.c')}$`).test('abc')).toBe(false)
  })

  it('keeps every other character, including a slash, a hyphen, and a line break', () => {
    expect(escapeRegExp('path/to-file\nnext line é')).toBe('path/to-file\nnext line é')
  })

  it('returns an empty text for an empty text', () => {
    expect(escapeRegExp('')).toBe('')
  })
})
