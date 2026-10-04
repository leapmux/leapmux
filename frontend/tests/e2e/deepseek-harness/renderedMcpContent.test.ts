import { afterEach, describe, expect, it, vi } from 'vitest'
import { largeMarkdownPlainHtml, LIMITED_TEXT_DISPLAY_NOTICE, PLAIN_TEXT_DISPLAY_NOTICE } from '../../../src/components/chat/safeTextDisplay'
import { deepseekHarnessMcpTextDisplay, deepseekHarnessRenderedMcpContent } from './renderedMcpContent'

const displayNotices = [LIMITED_TEXT_DISPLAY_NOTICE, PLAIN_TEXT_DISPLAY_NOTICE]

afterEach(() => {
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

function result(markup: string): HTMLElement {
  const root = document.createElement('div')
  root.innerHTML = markup
  document.body.append(root)
  const rectangle = new DOMRect(0, 0, 64, 64)
  const rectangles = Object.assign([rectangle], { item: (index: number) => index === 0 ? rectangle : null })
  for (const element of root.querySelectorAll<HTMLElement>('*'))
    vi.spyOn(element, 'getClientRects').mockReturnValue(rectangles)
  return root
}

const image = '<button aria-label="Open image"><img src="data:image/png;base64,REPEATED"></button>'

describe('deepseekHarnessRenderedMcpContent', () => {
  it('reports no attached result when the locator has no matches', () => {
    expect(deepseekHarnessRenderedMcpContent([], [])).toBeNull()
  })

  it('reports no attached result when every matched row detached', () => {
    expect(deepseekHarnessRenderedMcpContent([document.createElement('div')], [])).toBeNull()
  })

  it('reads actual short paragraphs, large text, and repeated images in their rendered order', () => {
    const middle = 'native line\n'.repeat(1200)
    const root = result(`<p>First native text.</p>${image}${largeMarkdownPlainHtml(middle)}${image}<p>Last native text.</p>`)
    const largeText = `${'native line\n'.repeat(750)}… lines omitted from display …\n${'native line\n'.repeat(249)}`
    expect(root.querySelector('[data-large-text-display]')?.textContent).toBe(largeText)
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'text', text: 'First native text.' },
      { type: 'image', index: 0 },
      { type: 'text', text: largeText },
      { type: 'image', index: 1 },
      { type: 'text', text: 'Last native text.' },
    ])
  })

  it('detects a moved image even when both images contain the same bytes', () => {
    const root = result(`<p>First native text.</p>${image}<p>Middle native text.</p>${image}<p>Last native text.</p>`)
    const original = deepseekHarnessRenderedMcpContent(root, displayNotices)
    const first = root.querySelector('button')
    const last = root.lastElementChild
    if (!first || !last)
      throw new Error('The rendered order fixture requires its image and final text.')
    last.before(first)
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'text', text: 'First native text.' },
      { type: 'text', text: 'Middle native text.' },
      { type: 'image', index: 0 },
      { type: 'image', index: 1 },
      { type: 'text', text: 'Last native text.' },
    ])
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).not.toEqual(original)
  })

  it('preserves repeated text, inline text, and whitespace without merging separate blocks', () => {
    const root = result(`<p>Repeat <strong>native</strong> text.</p>${image}<p>Repeat <strong>native</strong> text.</p><pre data-large-text-display>  first\nlast  </pre><p></p>`)
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'text', text: 'Repeat native text.' },
      { type: 'image', index: 0 },
      { type: 'text', text: 'Repeat native text.' },
      { type: 'text', text: '  first\nlast  ' },
    ])
  })

  it.each(displayNotices)('excludes only the adjacent actual display notice: %s', (notice) => {
    const root = result(`<p>${notice}</p><pre data-large-text-display>Actual native text.</pre><p>${notice}</p><p>${notice}</p>${image}<p>${notice}</p>`)
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'text', text: notice },
      { type: 'text', text: 'Actual native text.' },
      { type: 'text', text: notice },
      { type: 'image', index: 0 },
      { type: 'text', text: notice },
    ])
  })

  it('keeps an adjacent paragraph when it does not contain a display notice', () => {
    const root = result('<pre data-large-text-display>Actual native text.</pre><p>Another native paragraph.</p>')
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'text', text: 'Actual native text.' },
      { type: 'text', text: 'Another native paragraph.' },
    ])
  })

  it.each(['hidden', 'aria-hidden="true"', 'style="display:none"', 'style="visibility:hidden"', 'style="visibility:collapse"'])('ignores a hidden copy under %s', (attribute) => {
    const root = result(`<div ${attribute}><p>Hidden native text.</p>${image}</div><p>Visible native text.</p>${image}`)
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'text', text: 'Visible native text.' },
      { type: 'image', index: 0 },
    ])
  })

  it('ignores unmeasured copies and images outside the actual image control', () => {
    const root = result(`<p>Unmeasured native text.</p>${image}<p>Visible native text.</p><img src="decorative"><button aria-label="Another control"><img src="unrelated"></button>`)
    const unmeasured = root.firstElementChild
    if (!unmeasured)
      throw new Error('The rendered order fixture requires its unmeasured text.')
    const rectangles = Object.assign([], { item: () => null })
    vi.mocked(unmeasured.getClientRects).mockReturnValue(rectangles)
    expect(deepseekHarnessRenderedMcpContent(root, displayNotices)).toEqual([
      { type: 'image', index: 0 },
      { type: 'text', text: 'Visible native text.' },
    ])
  })

  it('detects a missing image instead of inferring it from the control', () => {
    const root = result('<p>First native text.</p><button aria-label="Open image"></button><p>Last native text.</p>')
    const actual = deepseekHarnessRenderedMcpContent(root, displayNotices)
    expect(actual).toEqual([{ type: 'text', text: 'First native text.' }, { type: 'text', text: 'Last native text.' }])
    expect(actual).not.toEqual([{ type: 'text', text: 'First native text.' }, { type: 'image', index: 0 }, { type: 'text', text: 'Last native text.' }])
  })

  it('distinguishes an empty result from a detached result', () => {
    expect(deepseekHarnessRenderedMcpContent(result('<p></p>'), displayNotices)).toEqual([])
    const detached = result(`<p>Detached native text.</p>${image}`)
    detached.remove()
    expect(deepseekHarnessRenderedMcpContent(detached, displayNotices)).toBeNull()
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

  it('keeps a large native block in the exact plain-text display', () => {
    const native = `First line.\n\n${'x'.repeat(33000)}`
    expect(deepseekHarnessMcpTextDisplay(native)).toEqual([{ type: 'text', text: native }])
  })
})
