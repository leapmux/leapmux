import { describe, expect, it } from 'vitest'
import { afterLineIndent, escapeRegExp } from './regexp'

const SYNTAX_CHARACTERS = [
  '.',
  '*',
  '+',
  '?',
  '^',
  '$',
  '{',
  '}',
  '(',
  ')',
  '|',
  '[',
  ']',
  '\\',
]

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

describe('afterLineIndent', () => {
  const LINE_SEPARATOR = String.fromCharCode(0x2028)
  const PARAGRAPH_SEPARATOR = String.fromCharCode(0x2029)

  it('matches the body after the indentation of a later line', () => {
    expect(afterLineIndent(/foo/).test('x\n \tfoo')).toBe(true)
  })

  it('does not match the body after other text on its line', () => {
    expect(afterLineIndent(/foo/).test('x foo')).toBe(false)
  })

  it('applies the indentation to every alternative of the body', () => {
    expect(afterLineIndent(/a|b/).test('x b')).toBe(false)
    expect(afterLineIndent(/a|b/).test('  b')).toBe(true)
  })

  it('keeps the flags of the body and adds the multiline flag', () => {
    const pattern = afterLineIndent(/foo/i)
    expect(pattern.flags).toBe('im')
    expect(pattern.test('x\n  FOO')).toBe(true)
  })

  it('keeps a body that is already multiline', () => {
    const pattern = afterLineIndent(/foo$/m)
    expect(pattern.flags).toBe('m')
    expect(pattern.test('  foo\nbar')).toBe(true)
  })

  it('counts every whitespace character but a line terminator as indentation', () => {
    const indentation = `\t\v\f ${String.fromCharCode(0xA0)}${String.fromCharCode(0xFEFF)}${String.fromCharCode(0x3000)}`
    expect(afterLineIndent(/foo/).exec(`${indentation}foo`)?.index).toBe(0)
  })

  it.each([
    ['a line feed', '\n'],
    ['a carriage return', '\r'],
    ['a CRLF', '\r\n'],
    ['a line separator', LINE_SEPARATOR],
    ['a paragraph separator', PARAGRAPH_SEPARATOR],
  ])('ends the indentation at %s, so the match starts on the line of the body', (_name, terminator) => {
    const text = `x${terminator}${terminator}  foo`
    expect(afterLineIndent(/foo/).exec(text)?.index).toBe(text.length - '  foo'.length)
  })

  it('keeps the capture groups of the body', () => {
    expect(afterLineIndent(/(fo)(o)/).exec('  foo')?.slice(1)).toEqual(['fo', 'o'])
  })
})
