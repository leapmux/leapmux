import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { containsAnsi, escapeHtml, renderAnsi, stripAnsi } from './renderAnsi'
import { shikiHighlighter } from './renderMarkdown'
import { _resetShikiStyleClassesForTest } from './shikiStyleClass'
import { readInjectedShikiRules } from './shikiStyleClass.testkit'

const ESC = '\x1B'
const RED = `${ESC}[31m`
const GREEN = `${ESC}[32m`
const RESET = `${ESC}[0m`
const SPAN_TAG_RE = /<span/g

describe('containsansi', () => {
  it('detects a foreground color escape', () => {
    expect(containsAnsi(`${RED}hello${RESET}`)).toBe(true)
  })

  it('detects a bare style escape with no parameters', () => {
    expect(containsAnsi(`${ESC}[m`)).toBe(true)
  })

  it('detects a lone reset SGR escape', () => {
    expect(containsAnsi(`${ESC}[0m`)).toBe(true)
  })

  it('detects a multi-parameter SGR escape (e.g. bold + bright red)', () => {
    expect(containsAnsi(`${ESC}[1;91mhello${RESET}`)).toBe(true)
  })

  it('detects a 256-color SGR escape', () => {
    expect(containsAnsi(`${ESC}[38;5;196mred${RESET}`)).toBe(true)
  })

  it('detects a truecolor (24-bit) SGR escape', () => {
    expect(containsAnsi(`${ESC}[38;2;255;0;0mred${RESET}`)).toBe(true)
  })

  it('detects a lone bold SGR escape', () => {
    expect(containsAnsi(`${ESC}[1mbold${RESET}`)).toBe(true)
  })

  it('detects a lone underline SGR escape', () => {
    expect(containsAnsi(`${ESC}[4munderline${RESET}`)).toBe(true)
  })

  it('returns false for plain text', () => {
    expect(containsAnsi('hello world')).toBe(false)
  })

  it('returns false for an empty string', () => {
    expect(containsAnsi('')).toBe(false)
  })

  it('detects non-SGR CSI control escapes that the stripper removes', () => {
    expect(containsAnsi(`${ESC}[A`)).toBe(true)
    expect(containsAnsi(`${ESC}[2K`)).toBe(true)
  })

  it('detects OSC control escapes that the stripper removes', () => {
    expect(containsAnsi(`${ESC}]8;;https://example.com${ESC}\\link${ESC}]8;;${ESC}\\`)).toBe(true)
  })
})

describe('renderansi', () => {
  it('produces a <pre><code> wrapper for ANSI-bearing input', () => {
    const html = renderAnsi(`${RED}error${RESET}`)
    expect(html).toMatch(/<pre[^>]*>/)
    expect(html).toMatch(/<\/pre>/)
    expect(html).toContain('error')
  })

  it('produces a <pre class="shiki"> root for colored text', () => {
    const html = renderAnsi(`${RED}red text${RESET}`)
    expect(html).toContain('class="shiki')
    expect(html).toContain('red text')
  })

  it('produces CSS variable-based styles for colored text', () => {
    const html = renderAnsi(`${RED}red${RESET}`)
    expect(html).toContain('--shiki-light:')
    expect(html).toContain('--shiki-dark:')
  })

  it('preserves the surrounding text when ANSI is mixed with plain content', () => {
    const html = renderAnsi(`prefix ${GREEN}ok${RESET} suffix`)
    expect(html).toContain('prefix')
    expect(html).toContain('ok')
    expect(html).toContain('suffix')
  })

  it('produces output for plain text (no ANSI sequences)', () => {
    const html = renderAnsi('plain output')
    expect(html).toMatch(/<pre[^>]*>/)
    expect(html).toContain('plain output')
  })

  it('escapes HTML characters in content via the shiki path', () => {
    const html = renderAnsi('<script>alert("xss")</script>')
    expect(html).not.toContain('<script>')
    // Shiki uses XML numeric entities (&#x3C;) for '<'
    expect(html).toContain('&#x3C;script>')
  })

  it('emits at least one styled span per color across a multi-color string', () => {
    const html = renderAnsi(`${RED}red${RESET} ${GREEN}green${RESET}`)
    expect(html).toContain('red')
    expect(html).toContain('green')
    // Each color yields its own token span.
    const spanCount = (html.match(SPAN_TAG_RE) || []).length
    expect(spanCount).toBeGreaterThanOrEqual(2)
  })

  it('renders background colors with --shiki-*-bg variables', () => {
    const html = renderAnsi(`${ESC}[41mred bg${RESET}`)
    expect(html).toContain('red bg')
    // Background colors produce --shiki-light-bg / --shiki-dark-bg variables
    expect(html).toContain('--shiki-light-bg:')
    expect(html).toContain('--shiki-dark-bg:')
  })

  it('does not include the raw ESC character in the rendered HTML', () => {
    const html = renderAnsi(`${RED}colored${RESET}`)
    expect(html.includes('\x1B')).toBe(false)
  })

  it('strips non-SGR CSI controls before rendering', () => {
    const html = renderAnsi(`before${ESC}[2Kafter${ESC}[A`)
    expect(html.includes(ESC)).toBe(false)
    expect(html).toContain('before')
    expect(html).toContain('after')
  })

  it('strips OSC controls before rendering while preserving visible text', () => {
    const html = renderAnsi(`before${ESC}]8;;https://example.com${ESC}\\link${ESC}]8;;${ESC}\\after`)
    expect(html.includes(ESC)).toBe(false)
    expect(html).toContain('before')
    expect(html).toContain('link')
    expect(html).toContain('after')
  })

  describe('fallback on shiki error', () => {
    afterEach(() => {
      vi.restoreAllMocks()
    })

    it('falls back to escaped HTML when shiki throws', () => {
      vi.spyOn(shikiHighlighter, 'codeToHtml').mockImplementation(() => {
        throw new Error('regex engine failure')
      })

      const html = renderAnsi('<b>bold</b>')
      expect(html).toContain('<pre><code>')
      expect(html).toContain('&lt;b&gt;bold&lt;/b&gt;')
      expect(html).not.toContain('<b>')
    })

    it('strips terminal controls from fallback HTML', () => {
      vi.spyOn(shikiHighlighter, 'codeToHtml').mockImplementation(() => {
        throw new Error('regex engine failure')
      })

      const html = renderAnsi(`before${ESC}[2K<script>after</script>`)
      expect(html.includes(ESC)).toBe(false)
      expect(html).toContain('before&lt;script&gt;after&lt;/script&gt;')
    })
  })
})

describe('renderansi shared token-style classes', () => {
  beforeEach(() => {
    _resetShikiStyleClassesForTest()
  })

  it('emits class-based token spans (no inline span styles) and injects the rules', () => {
    const html = renderAnsi(`${GREEN}green${RESET} plain`)
    // Token spans carry shared classes; only the <pre> keeps its rootStyle.
    expect(html).toContain('class="sk-')
    expect(html).not.toContain('<span style=')
    // Every referenced class has an injected dual-theme rule (this path runs on
    // the main thread, so the transformer injects directly -- see shikiStyleClass).
    const rules = readInjectedShikiRules()
    const classes = [...html.matchAll(/class="(sk-[0-9a-z-]+)"/g)].map(m => m[1])
    expect(classes.length).toBeGreaterThan(0)
    for (const className of new Set(classes))
      expect(rules).toContain(`.${className}{`)
    expect(rules).toContain('--shiki-light')
  })

  it('keeps the visible payload intact across the class conversion', () => {
    const raw = `${RED}red${RESET} tail`
    const html = renderAnsi(raw)
    const textOnly = html.replace(/<[^>]+>/g, '')
    expect(textOnly).toBe(stripAnsi(raw))
  })
})

describe('stripansi', () => {
  it('strips CSI controls beyond SGR while preserving printable text', () => {
    expect(stripAnsi(`${GREEN}ok${RESET}${ESC}[2K\rnext${ESC}[A`)).toBe('ok\rnext')
  })

  it('strips OSC controls while preserving surrounding text', () => {
    expect(stripAnsi(`before${ESC}]8;;https://example.com${ESC}\\link${ESC}]8;;${ESC}\\after`)).toBe('beforelinkafter')
  })
})

describe('escapehtml', () => {
  it('escapes & < > so user-supplied text is safe to inject into HTML', () => {
    expect(escapeHtml('a & b')).toBe('a &amp; b')
    expect(escapeHtml('<script>')).toBe('&lt;script&gt;')
    expect(escapeHtml('1 < 2 && 3 > 2')).toBe('1 &lt; 2 &amp;&amp; 3 &gt; 2')
  })

  it('does not escape quotes or apostrophes (out of scope)', () => {
    expect(escapeHtml(`it's "quoted"`)).toBe(`it's "quoted"`)
  })

  it('returns the empty string unchanged', () => {
    expect(escapeHtml('')).toBe('')
  })
})
