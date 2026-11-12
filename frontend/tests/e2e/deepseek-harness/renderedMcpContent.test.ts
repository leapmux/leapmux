import { afterEach, describe, expect, it, vi } from 'vitest'
import { requestMarkdownHtml } from '../../../src/lib/renderMarkdown'
import { deepseekHarnessMcpTextDisplay, deepseekHarnessRenderedMcpContent } from './renderedMcpContent'

/** The size above which Markdown used to show as capped plain text. Every size now renders as Markdown. */
const FORMER_PLAIN_DISPLAY_CHARS = 32 * 1024

/** The HTML that the actual Markdown renderer shows for `text`. jsdom has no Worker, so it renders synchronously. */
function renderedMarkdown(text: string): string {
  const html = requestMarkdownHtml(text)
  if (html === undefined)
    throw new Error('Markdown rendered asynchronously in a test without a Worker.')
  return html
}

afterEach(() => {
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

function result(markup: string): HTMLElement {
  const root = document.createElement('div')
  root.innerHTML = markup
  document.body.append(root)
  const rectangle = new DOMRect(0, 0, 64, 64)
  const rectangles = Object.assign([rectangle], {
    item: (index: number) => index === 0
      ? rectangle
      : null,
  })
  for (const element of root.querySelectorAll<HTMLElement>('*'))
    vi.spyOn(element, 'getClientRects').mockReturnValue(rectangles)
  return root
}

const image = '<button aria-label="Open image"><img src="data:image/png;base64,REPEATED"></button>'

describe('deepseekHarnessRenderedMcpContent', () => {
  it('reports no attached result when the locator has no matches', () => {
    expect(deepseekHarnessRenderedMcpContent([])).toBeNull()
  })

  it('reports no attached result when every matched row detached', () => {
    expect(deepseekHarnessRenderedMcpContent([
      document.createElement('div'),
    ])).toBeNull()
  })

  it('reads actual short paragraphs, a large paragraph, and repeated images in their rendered order', () => {
    const middle = 'native line\n'.repeat(1200)
    const root = result(`<p>First native text.</p>${image}${renderedMarkdown(middle)}${image}<p>Last native text.</p>`)
    // A large paragraph renders whole, every line kept: no omission and no cap.
    const largeText = Array.from({ length: 1200 }).fill('native line').join('\n')
    expect(deepseekHarnessRenderedMcpContent(root)).toEqual([
      { type: 'text', text: 'First native text.' },
      { type: 'image', index: 0 },
      { type: 'text', text: largeText },
      { type: 'image', index: 1 },
      { type: 'text', text: 'Last native text.' },
    ])
  })

  it('detects a moved image even when both images contain the same bytes', () => {
    const root = result(`<p>First native text.</p>${image}<p>Middle native text.</p>${image}<p>Last native text.</p>`)
    const original = deepseekHarnessRenderedMcpContent(root)
    const first = root.querySelector('button')
    const last = root.lastElementChild
    if (!first || !last)
      throw new Error('The rendered order fixture requires its image and final text.')
    last.before(first)
    expect(deepseekHarnessRenderedMcpContent(root)).toEqual([
      { type: 'text', text: 'First native text.' },
      { type: 'text', text: 'Middle native text.' },
      { type: 'image', index: 0 },
      { type: 'image', index: 1 },
      { type: 'text', text: 'Last native text.' },
    ])
    expect(deepseekHarnessRenderedMcpContent(root)).not.toEqual(original)
  })

  it('preserves repeated text, inline text, and whitespace without merging separate blocks', () => {
    const root = result(`<p>Repeat <strong>native</strong> text.</p>${image}<p>Repeat <strong>native</strong> text.</p><p>  first\nlast  </p><p></p>`)
    expect(deepseekHarnessRenderedMcpContent(root)).toEqual([
      { type: 'text', text: 'Repeat native text.' },
      { type: 'image', index: 0 },
      { type: 'text', text: 'Repeat native text.' },
      { type: 'text', text: '  first\nlast  ' },
    ])
  })

  it.each([
    'hidden',
    'aria-hidden="true"',
    'style="display:none"',
    'style="visibility:hidden"',
    'style="visibility:collapse"',
  ])('ignores a hidden copy under %s', (attribute) => {
    const root = result(`<div ${attribute}><p>Hidden native text.</p>${image}</div><p>Visible native text.</p>${image}`)
    expect(deepseekHarnessRenderedMcpContent(root)).toEqual([
      { type: 'text', text: 'Visible native text.' },
      { type: 'image', index: 0 },
    ])
  })

  it('ignores unmeasured copies and images outside the actual image control', () => {
    const root = result(`<p>Unmeasured native text.</p>${image}<p>Visible native text.</p><img src="decorative"><button aria-label="Another control"><img src="unrelated"></button>`)
    const unmeasured = root.firstElementChild
    if (!unmeasured)
      throw new Error('The rendered order fixture requires its unmeasured text.')
    const rectangles = Object.assign([], {
      item: () => null,
    })
    vi.mocked(unmeasured.getClientRects).mockReturnValue(rectangles)
    expect(deepseekHarnessRenderedMcpContent(root)).toEqual([
      { type: 'image', index: 0 },
      { type: 'text', text: 'Visible native text.' },
    ])
  })

  it('detects a missing image instead of inferring it from the control', () => {
    const root = result('<p>First native text.</p><button aria-label="Open image"></button><p>Last native text.</p>')
    const actual = deepseekHarnessRenderedMcpContent(root)
    expect(actual).toEqual([
      { type: 'text', text: 'First native text.' },
      { type: 'text', text: 'Last native text.' },
    ])
    expect(actual).not.toEqual([
      { type: 'text', text: 'First native text.' },
      { type: 'image', index: 0 },
      { type: 'text', text: 'Last native text.' },
    ])
  })

  it('distinguishes an empty result from a detached result', () => {
    expect(deepseekHarnessRenderedMcpContent(result('<p></p>'))).toEqual([])
    const detached = result(`<p>Detached native text.</p>${image}`)
    detached.remove()
    expect(deepseekHarnessRenderedMcpContent(detached)).toBeNull()
  })
})

describe('deepseekHarnessMcpTextDisplay', () => {
  it('keeps an empty native text block absent from the view', () => {
    expect(deepseekHarnessMcpTextDisplay('')).toEqual([])
  })

  it('keeps the exact native tail and omission notice as separate Markdown paragraphs', () => {
    const tail = 'NATIVETOOLOUTPUT-complete-42'
    const notice = '(Omitted 672056 bytes. Full formatted result stored at: /private/native/mcp__results__inspect.txt. Use read with offset/limit, or grep this path to search within it.)'
    const native = `${tail}\n\n${notice}`
    expect(deepseekHarnessMcpTextDisplay(native)).toEqual([
      { type: 'text', text: tail },
      { type: 'text', text: notice },
    ])
  })

  it('keeps repeated native paragraphs as separate occurrences', () => {
    expect(deepseekHarnessMcpTextDisplay('0 false\n\n0 false')).toEqual([
      { type: 'text', text: '0 false' },
      { type: 'text', text: '0 false' },
    ])
  })

  it('keeps a single newline inside its original paragraph', () => {
    expect(deepseekHarnessMcpTextDisplay('First line.\nLast line.')).toEqual([
      { type: 'text', text: 'First line.\nLast line.' },
    ])
  })

  it('parses a large native block into its whole Markdown paragraphs, with no cap', () => {
    const native = `First line.\n\n${'x'.repeat(33000)}`
    expect(deepseekHarnessMcpTextDisplay(native)).toEqual([
      { type: 'text', text: 'First line.' },
      {
        type: 'text',
        text: 'x'.repeat(33000),
      },
    ])
  })

  it('parses Markdown paragraphs the same way at, above, and far above the former plain-display size', () => {
    const sized = (length: number) => {
      const first = 'First paragraph.'
      const line = `${'y'.repeat(99)}\n`
      const available = length - first.length - 2
      const body = `${line.repeat(Math.floor(available / line.length))}${'y'.repeat(available % line.length)}`
      return { native: `${first}\n\n${body}`, first, body }
    }
    for (const length of [FORMER_PLAIN_DISPLAY_CHARS, FORMER_PLAIN_DISPLAY_CHARS + 1, 8 * FORMER_PLAIN_DISPLAY_CHARS]) {
      const fixture = sized(length)
      expect(fixture.native).toHaveLength(length)
      expect(deepseekHarnessMcpTextDisplay(fixture.native)).toEqual([
        { type: 'text', text: fixture.first },
        { type: 'text', text: fixture.body },
      ])
    }
  })

  it.each([
    { structure: 'a heading', native: '# Native heading' },
    { structure: 'a list', native: '- native item' },
    { structure: 'emphasis', native: 'Native **strong** text' },
    { structure: 'inline code', native: 'Native `code` text' },
    { structure: 'a hard line break', native: 'First line.  \nLast line.' },
    { structure: 'a code block', native: '```\nnative\n```' },
  ])('refuses short fixture text that contains $structure', ({ native }) => {
    expect(() => deepseekHarnessMcpTextDisplay(native)).toThrow('unsupported')
  })

  it('matches the paragraphs and images that the actual Markdown renderer shows', () => {
    const tail = 'NATIVETOOLOUTPUT-complete-42'
    const notice = '(Omitted 672056 bytes. Full formatted result stored at: /private/native/mcp__results__inspect.txt. Use read with offset/limit, or grep this path to search within it.)'
    const blocks = ['0 false\n\n0 false', `${tail}\n\n${notice}`, 'First line.\nLast line.']
    const root = result(`${renderedMarkdown(blocks[0]!)}${image}${renderedMarkdown(blocks[1]!)}${image}${renderedMarkdown(blocks[2]!)}`)
    expect(deepseekHarnessRenderedMcpContent(root)).toEqual([
      ...deepseekHarnessMcpTextDisplay(blocks[0]!),
      { type: 'image', index: 0 },
      ...deepseekHarnessMcpTextDisplay(blocks[1]!),
      { type: 'image', index: 1 },
      ...deepseekHarnessMcpTextDisplay(blocks[2]!),
    ])
    expect(deepseekHarnessRenderedMcpContent(root)).toHaveLength(7)
  })
})
