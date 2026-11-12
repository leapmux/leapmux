import { describe, expect, it } from 'vitest'
import {
  MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE,
  MARKDOWN_SOURCE_FALLBACK_CLASS,
  MARKDOWN_SOURCE_FALLBACK_NOTICE,
  markdownSourceFallbackHtml,
} from './markdownSourceFallback'

/** Parse the fallback HTML the way the browser applies it. */
function fallbackDom(text: string): HTMLElement {
  const root = document.createElement('div')
  root.innerHTML = markdownSourceFallbackHtml(text)
  return root
}

describe('markdownSourceFallbackHtml', () => {
  it('shows the source verbatim in a marked pre, followed by the notice', () => {
    const source = '# Heading\n\n- item **bold**\n'
    const root = fallbackDom(source)

    const pre = root.querySelector('pre')!
    expect(pre.classList.contains(MARKDOWN_SOURCE_FALLBACK_CLASS)).toBe(true)
    expect(pre.hasAttribute(MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE)).toBe(true)
    expect(pre.textContent).toBe(source)
    expect(root.querySelector('p')?.textContent).toBe(MARKDOWN_SOURCE_FALLBACK_NOTICE)
    // Nothing was parsed as Markdown.
    expect(root.querySelector('h1, ul, strong')).toBeNull()
  })

  it('escapes markup so that the source can never create an element', () => {
    const source = '<script>alert(1)</script><img src="x" onerror="alert(2)"> & </pre><p>'
    const root = fallbackDom(source)

    expect(root.querySelector('script, img')).toBeNull()
    expect(root.querySelectorAll('pre')).toHaveLength(1)
    expect(root.querySelector('pre')?.textContent).toBe(source)
  })

  it('keeps an empty source as an empty pre', () => {
    const root = fallbackDom('')
    expect(root.querySelector('pre')?.textContent).toBe('')
    expect(root.querySelector('p')?.textContent).toBe(MARKDOWN_SOURCE_FALLBACK_NOTICE)
  })

  it('keeps every character of a very large source, with no cap', () => {
    const source = `${'line of source\n'.repeat(70_000)}end`
    expect(fallbackDom(source).querySelector('pre')?.textContent).toBe(source)
  })

  it('keeps astral characters and surrogate pairs whole', () => {
    const source = 'emoji 😀 and 𝒳'
    expect(fallbackDom(source).querySelector('pre')?.textContent).toBe(source)
  })
})
