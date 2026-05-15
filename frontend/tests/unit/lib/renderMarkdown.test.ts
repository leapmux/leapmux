import { beforeEach, describe, expect, it } from 'vitest'
import { _getMarkdownCacheSize, _resetMarkdownCache, renderMarkdown } from '~/lib/renderMarkdown'

describe('renderMarkdown', () => {
  beforeEach(() => {
    _resetMarkdownCache()
  })

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
    expect(_getMarkdownCacheSize()).toBe(0)
    const html1 = renderMarkdown('cached test')
    expect(_getMarkdownCacheSize()).toBe(1)
    const html2 = renderMarkdown('cached test')
    // Second call must hit the cache — size stays at 1, not 2.
    expect(_getMarkdownCacheSize()).toBe(1)
    expect(html1).toBe(html2)
  })

  it('should bypass cache when skipCache is true', () => {
    expect(_getMarkdownCacheSize()).toBe(0)
    renderMarkdown('cached test', true)
    expect(_getMarkdownCacheSize()).toBe(0)
  })
})
