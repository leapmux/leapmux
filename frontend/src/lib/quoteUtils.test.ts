import { describe, expect, it } from 'vitest'
import { formatChatQuote, formatFileMention, formatFileQuote, nodeToMarkdown } from './quoteUtils'

/** Helper: parse an HTML string into a DocumentFragment for nodeToMarkdown tests. */
function html(str: string): DocumentFragment {
  const template = document.createElement('template')
  template.innerHTML = str
  return template.content
}

describe('formatFileQuote', () => {
  it('formats single-line selection with @mention and (Line N)', () => {
    expect(formatFileQuote('src/main.ts', 10, 10, 'const x = 1')).toBe(
      'From @src/main.ts (Line 10):\n\n> ```\n> const x = 1\n> ```',
    )
  })

  it('formats multi-line selection with @mention and (Line N-M)', () => {
    expect(formatFileQuote('src/main.ts', 10, 12, 'line1\nline2\nline3')).toBe(
      'From @src/main.ts (Line 10-12):\n\n> ```\n> line1\n> line2\n> line3\n> ```',
    )
  })

  it('preserves empty lines in selected text', () => {
    expect(formatFileQuote('test.ts', 1, 3, 'a\n\nb')).toBe(
      'From @test.ts (Line 1-3):\n\n> ```\n> a\n> \n> b\n> ```',
    )
  })
})

describe('formatChatQuote', () => {
  it('wraps single-line text as blockquote', () => {
    expect(formatChatQuote('hello')).toBe('> hello\n\n')
  })

  it('wraps multi-line text as blockquote', () => {
    expect(formatChatQuote('hello\nworld')).toBe('> hello\n> world\n\n')
  })

  it('handles empty lines', () => {
    expect(formatChatQuote('a\n\nb')).toBe('> a\n> \n> b\n\n')
  })
})

describe('formatFileMention', () => {
  it('prefixes path with @', () => {
    expect(formatFileMention('src/main.ts')).toBe('@src/main.ts')
  })

  it('works with simple filename', () => {
    expect(formatFileMention('package.json')).toBe('@package.json')
  })
})

describe('nodeToMarkdown', () => {
  it('returns plain text from a text node', () => {
    expect(nodeToMarkdown(html('hello world'))).toBe('hello world')
  })

  it('converts <strong> to **bold**', () => {
    expect(nodeToMarkdown(html('<strong>bold</strong>'))).toBe('**bold**')
  })

  it('converts <b> to **bold**', () => {
    expect(nodeToMarkdown(html('<b>bold</b>'))).toBe('**bold**')
  })

  it('converts <em> to *italic*', () => {
    expect(nodeToMarkdown(html('<em>italic</em>'))).toBe('*italic*')
  })

  it('converts <i> to *italic*', () => {
    expect(nodeToMarkdown(html('<i>italic</i>'))).toBe('*italic*')
  })

  it('converts inline <code> to backticks', () => {
    expect(nodeToMarkdown(html('<code>foo()</code>'))).toBe('`foo()`')
  })

  it('converts <del> / <s> to ~~strikethrough~~', () => {
    expect(nodeToMarkdown(html('<del>removed</del>'))).toBe('~~removed~~')
    expect(nodeToMarkdown(html('<s>removed</s>'))).toBe('~~removed~~')
  })

  it('converts <a> to [text](href)', () => {
    expect(nodeToMarkdown(html('<a href="https://example.com">link</a>')))
      .toBe('[link](https://example.com)')
  })

  it('handles <a> without href', () => {
    expect(nodeToMarkdown(html('<a>text</a>'))).toBe('text')
  })

  it('converts <br> to newline', () => {
    expect(nodeToMarkdown(html('a<br>b'))).toBe('a\nb')
  })

  it('converts <p> to paragraph with trailing double newline', () => {
    expect(nodeToMarkdown(html('<p>first</p><p>second</p>')))
      .toBe('first\n\nsecond\n\n')
  })

  it('converts headings to # prefixed lines', () => {
    expect(nodeToMarkdown(html('<h1>Title</h1>'))).toBe('# Title\n\n')
    expect(nodeToMarkdown(html('<h2>Sub</h2>'))).toBe('## Sub\n\n')
    expect(nodeToMarkdown(html('<h3>H3</h3>'))).toBe('### H3\n\n')
  })

  it('converts <hr> to ---', () => {
    expect(nodeToMarkdown(html('<hr>'))).toBe('\n---\n')
  })

  it('converts unordered list', () => {
    expect(nodeToMarkdown(html('<ul><li>a</li><li>b</li></ul>')))
      .toBe('- a\n- b\n\n')
  })

  it('converts ordered list', () => {
    expect(nodeToMarkdown(html('<ol><li>first</li><li>second</li></ol>')))
      .toBe('1. first\n2. second\n\n')
  })

  it('converts <pre><code> to fenced code block', () => {
    expect(nodeToMarkdown(html('<pre><code>x = 1</code></pre>')))
      .toBe('\n```\nx = 1\n```\n')
  })

  it('extracts language from code class in <pre>', () => {
    expect(nodeToMarkdown(html('<pre><code class="language-ts">const x = 1</code></pre>')))
      .toBe('\n```ts\nconst x = 1\n```\n')
  })

  it('converts <blockquote> to > prefixed lines', () => {
    expect(nodeToMarkdown(html('<blockquote><p>quoted text</p></blockquote>')))
      .toBe('> quoted text\n')
  })

  it('converts nested blockquote content', () => {
    expect(nodeToMarkdown(html('<blockquote><p>line 1</p><p>line 2</p></blockquote>')))
      .toBe('> line 1\n> \n> line 2\n')
  })

  it('handles nested inline formatting', () => {
    expect(nodeToMarkdown(html('<p>This is <strong>bold and <em>italic</em></strong> text</p>')))
      .toBe('This is **bold and *italic*** text\n\n')
  })

  it('converts <div> with trailing newline', () => {
    expect(nodeToMarkdown(html('<div>line</div>'))).toBe('line\n')
  })

  it('passes through unknown tags by rendering children', () => {
    expect(nodeToMarkdown(html('<span>inner</span>'))).toBe('inner')
  })

  it('converts blockquote containing a list', () => {
    expect(nodeToMarkdown(html('<blockquote><ul><li>a</li><li>b</li></ul></blockquote>')))
      .toBe('> - a\n> - b\n')
  })

  it('converts blockquote containing a code block', () => {
    expect(nodeToMarkdown(html('<blockquote><pre><code>x = 1</code></pre></blockquote>')))
      .toBe('> \n> ```\n> x = 1\n> ```\n')
  })

  it('converts nested blockquotes', () => {
    expect(nodeToMarkdown(html('<blockquote><blockquote><p>deep</p></blockquote></blockquote>')))
      .toBe('> > deep\n')
  })

  it('converts paragraph with inline code and bold mixed', () => {
    expect(nodeToMarkdown(html('<p>Use <code>foo()</code> with <strong>caution</strong></p>')))
      .toBe('Use `foo()` with **caution**\n\n')
  })

  it('converts list items with inline formatting', () => {
    expect(nodeToMarkdown(html('<ul><li><strong>bold</strong> item</li><li><em>italic</em> item</li></ul>')))
      .toBe('- **bold** item\n- *italic* item\n\n')
  })

  it('converts ordered list with code and links', () => {
    expect(nodeToMarkdown(html('<ol><li>Run <code>npm install</code></li><li>See <a href="https://example.com">docs</a></li></ol>')))
      .toBe('1. Run `npm install`\n2. See [docs](https://example.com)\n\n')
  })

  it('converts mixed paragraphs, code block, and list', () => {
    expect(nodeToMarkdown(html(
      '<p>Intro text</p>'
      + '<pre><code class="language-js">const x = 1</code></pre>'
      + '<ul><li>note 1</li><li>note 2</li></ul>',
    ))).toBe('Intro text\n\n\n```js\nconst x = 1\n```\n- note 1\n- note 2\n\n')
  })

  it('converts blockquote with paragraphs and a nested list', () => {
    expect(nodeToMarkdown(html(
      '<blockquote><p>Quote intro</p><ul><li>point A</li><li>point B</li></ul></blockquote>',
    ))).toBe('> Quote intro\n> \n> - point A\n> - point B\n')
  })
})
