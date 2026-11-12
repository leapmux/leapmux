import { describe, expect, it } from 'vitest'
import { hasMarkdownSyntax } from './inputPlugins'

const NBSP = String.fromCharCode(0xA0)
const LINE_SEPARATOR = String.fromCharCode(0x2028)

/**
 * Large enough that a scan quadratic in the run of blank lines runs for over a minute,
 * so the vitest timeout fails it; a linear scan takes a few milliseconds.
 */
const BLANK_LINE_COUNT = 200_000

describe('hasMarkdownSyntax', () => {
  it.each([
    ['a bullet', '- item'],
    ['an indented bullet', '  * item'],
    ['a bullet after blank lines', '\n\n   + item'],
    ['a bullet indented with a no-break space', `${NBSP}- item`],
    ['a heading', '# Heading'],
    ['a level-6 heading', '###### Heading'],
    ['an ordered item', '1. first'],
    ['a code fence', '```js\ncode\n```'],
    ['a blockquote', '> quote'],
    ['bold text', 'some **bold** text'],
    ['underscore bold text', 'some __bold__ text'],
    ['struck text', 'some ~~gone~~ text'],
    ['inline code', 'use `code` here'],
  ])('finds %s', (_name, text) => {
    expect(hasMarkdownSyntax(text)).toBe(true)
  })

  it.each([
    ['plain words', 'plain words'],
    ['a dash with no space after it', '-item'],
    ['an empty string', ''],
  ])('finds nothing in %s', (_name, text) => {
    expect(hasMarkdownSyntax(text)).toBe(false)
  })

  describe('a long run of blank lines', () => {
    it('finds nothing in linear time', () => {
      expect(hasMarkdownSyntax('    \n'.repeat(BLANK_LINE_COUNT))).toBe(false)
    })

    it('finds a bullet after the run', () => {
      expect(hasMarkdownSyntax(`${'    \n'.repeat(BLANK_LINE_COUNT)}  - item`)).toBe(true)
    })

    it('finds nothing in a run of line separators in linear time', () => {
      expect(hasMarkdownSyntax(`${LINE_SEPARATOR}    `.repeat(BLANK_LINE_COUNT))).toBe(false)
    })
  })
})
