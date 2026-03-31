import { describe, expect, it } from 'vitest'
import { renderMarkdown } from '~/lib/renderMarkdown'

describe('renderMarkdown', () => {
  it('should render markdown with syntax highlighting', () => {
    const html = renderMarkdown('```js\nconst x = 1\n```', true)
    expect(html).toContain('class="shiki')
    expect(html).toContain('const')
  })

  it('should render plain text', () => {
    const html = renderMarkdown('hello world', true)
    expect(html).toContain('hello world')
  })

  it('should render inline code', () => {
    const html = renderMarkdown('use `const x = 1`', true)
    expect(html).toContain('<code>')
    expect(html).toContain('const x = 1')
  })

  it('should render code blocks with unknown languages without crashing', () => {
    // Unknown language should fall back gracefully (no Shiki highlighting).
    const html = renderMarkdown('```unknownlang123\nfoo bar\n```', true)
    expect(html).toContain('foo bar')
  })

  it('should render GFM tables', () => {
    const md = '| a | b |\n|---|---|\n| 1 | 2 |'
    const html = renderMarkdown(md, true)
    expect(html).toContain('<table>')
    expect(html).toContain('<td>')
  })

  it('should cache results', () => {
    const html1 = renderMarkdown('cached test')
    const html2 = renderMarkdown('cached test')
    expect(html1).toBe(html2)
  })
})
