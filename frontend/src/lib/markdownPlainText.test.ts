import { describe, expect, it } from 'vitest'
import { markdownToPlainText } from './markdownPlainText'

describe('markdownToPlainText', () => {
  it('drops emphasis marks and keeps the words', () => {
    expect(markdownToPlainText('ship the **auth refactor**')).toBe('ship the auth refactor')
    expect(markdownToPlainText('_every_ test passes')).toBe('every test passes')
    expect(markdownToPlainText('~~drop~~ this')).toBe('drop this')
  })

  it('keeps the text inside a code span and a fence', () => {
    expect(markdownToPlainText('run `task test` first')).toBe('run task test first')
    expect(markdownToPlainText('```sh\nbun run lint\n```')).toBe('bun run lint')
  })

  it('reads a link as its label, not its URL', () => {
    expect(markdownToPlainText('see [the plan](https://example.com/p)')).toBe('see the plan')
  })

  it('reads an image as its alt text', () => {
    expect(markdownToPlainText('![a chart](chart.png)')).toBe('a chart')
  })

  it('separates list items so they do not run together', () => {
    expect(markdownToPlainText('- fix the parser\n- rerun CI')).toBe('fix the parser rerun CI')
  })

  it('separates paragraphs and headings', () => {
    expect(markdownToPlainText('# Goal\n\nShip it.\n\nThen rest.')).toBe('Goal Ship it. Then rest.')
  })

  it('collapses every block boundary to one line', () => {
    expect(markdownToPlainText('a\n\n\nb')).toBe('a b')
    expect(markdownToPlainText('> quoted\n\nafter')).toBe('quoted after')
  })

  /**
   * The renderer shows a raw-HTML run as literal text rather than dropping it,
   * so the spoken reading has to carry the same characters -- see
   * `remarkHtmlAsText` in `./markdownProcessor.ts`.
   */
  it('keeps a bare angle-bracket run, matching what the renderer shows', () => {
    expect(markdownToPlainText('Replace <old-token> with the new one'))
      .toBe('Replace <old-token> with the new one')
  })

  it('returns an empty string for empty and whitespace-only input', () => {
    expect(markdownToPlainText('')).toBe('')
    expect(markdownToPlainText('   \n  ')).toBe('')
  })

  it('leaves plain prose untouched', () => {
    expect(markdownToPlainText('every test passes')).toBe('every test passes')
  })

  it('reads a table row by row', () => {
    expect(markdownToPlainText('| a | b |\n| - | - |\n| 1 | 2 |')).toBe('a b 1 2')
  })
})
