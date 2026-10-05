import { describe, expect, it } from 'bun:test'
import { markdownBlockTexts, markdownLinks, markdownText, parseMarkdown, walkMarkdown } from './matrix-markdown.mjs'

describe('markdownBlockTexts', () => {
  it('returns one plain text for each top-level block', () => {
    expect(markdownBlockTexts('First **bold** paragraph.\n\nSecond paragraph.')).toEqual(['First  bold  paragraph.', 'Second paragraph.'])
  })

  it('returns no block for an empty note', () => {
    expect(markdownBlockTexts('')).toEqual([])
  })

  it('keeps the value of inline code and drops its backticks', () => {
    expect(markdownBlockTexts('Run `--flag` now.')).toEqual(['Run  --flag  now.'])
  })
})

describe('markdownText', () => {
  it('joins the blocks with a space, as the rendered page text does', () => {
    expect(markdownText('One.\n\nTwo.')).toBe('One. Two.')
  })

  it('reads the text of a link and never its target', () => {
    expect(markdownText('See [the issue](https://example.com/1).')).toBe('See  the issue .')
  })
})

describe('markdownLinks', () => {
  it('lists the target of each link in order, autolinks included', () => {
    expect(markdownLinks('[a](https://example.com/a) and <https://example.com/b> and [c](#c)'))
      .toEqual(['https://example.com/a', 'https://example.com/b', '#c'])
  })

  it('finds a link inside emphasis', () => {
    expect(markdownLinks('**[a](https://example.com/a)**')).toEqual(['https://example.com/a'])
  })

  it('lists no link for plain text', () => {
    expect(markdownLinks('No link here.')).toEqual([])
  })
})

describe('walkMarkdown', () => {
  it('visits a parent before its children', () => {
    const types = []
    walkMarkdown(parseMarkdown('A *b* c.'), node => types.push(node.type))
    expect(types).toEqual(['root', 'paragraph', 'text', 'emphasis', 'text', 'text'])
  })
})
