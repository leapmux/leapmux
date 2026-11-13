import rehypeStringify from 'rehype-stringify'
import remarkRehype from 'remark-rehype'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMarkdownParser } from '~/lib/markdownParse'
import { plainMarkdownProcessor } from '~/lib/markdownProcessor'
import { raiseStackOverflowFor } from './markdownParseOverflow'

const OVERFLOWING = '> deep <b>raw</b>'

describe('raiseStackOverflowFor', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('raises the RangeError of a stack overflow for its text, and processes any other text as before', () => {
    raiseStackOverflowFor(plainMarkdownProcessor, OVERFLOWING)

    expect(() => plainMarkdownProcessor.processSync(OVERFLOWING)).toThrow(RangeError)
    expect(() => plainMarkdownProcessor.processSync(OVERFLOWING)).toThrow('Maximum call stack size exceeded')
    expect(String(plainMarkdownProcessor.processSync('**bold**'))).toBe('<p><strong>bold</strong></p>')
  })

  // A real overflow of the shared parser fails every processor alike.
  it('raises it in every processor that shares the prototype, also one built after the spy', () => {
    raiseStackOverflowFor(plainMarkdownProcessor, OVERFLOWING)
    const other = createMarkdownParser().use(remarkRehype).use(rehypeStringify)

    expect(() => other.processSync(OVERFLOWING)).toThrow(RangeError)
    expect(String(other.processSync('*emphasis*'))).toBe('<p><em>emphasis</em></p>')
  })

  it('returns the spy, which counts the calls of every processor', () => {
    const processSync = raiseStackOverflowFor(plainMarkdownProcessor, OVERFLOWING)
    const other = createMarkdownParser().use(remarkRehype).use(rehypeStringify)

    expect(() => plainMarkdownProcessor.processSync(OVERFLOWING)).toThrow(RangeError)
    expect(() => other.processSync(OVERFLOWING)).toThrow(RangeError)
    other.processSync('plain')

    expect(processSync.mock.calls.map(([value]) => value)).toEqual([OVERFLOWING, OVERFLOWING, 'plain'])
  })

  it('leaves the processors alone once the spy is restored', () => {
    raiseStackOverflowFor(plainMarkdownProcessor, OVERFLOWING)

    vi.restoreAllMocks()

    expect(String(plainMarkdownProcessor.processSync(OVERFLOWING))).toContain('<blockquote>')
  })
})
