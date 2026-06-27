import { describe, expect, it } from 'vitest'
import { containsAnsi, escapeHtml, renderAnsi, stripAnsi } from './renderAnsi'

const ESC = '\x1B'
const RED = `${ESC}[31m`
const GREEN = `${ESC}[32m`
const RESET = `${ESC}[0m`

describe('containsAnsi', () => {
  it('detects a foreground color escape', () => {
    expect(containsAnsi(`${RED}hello${RESET}`)).toBe(true)
  })

  it('detects a bare style escape with no parameters', () => {
    expect(containsAnsi(`${ESC}[m`)).toBe(true)
  })

  it('detects a multi-parameter SGR escape (e.g. bold + bright red)', () => {
    expect(containsAnsi(`${ESC}[1;91mhello${RESET}`)).toBe(true)
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

describe('renderAnsi', () => {
  it('produces a <pre><code> wrapper for ANSI-bearing input', () => {
    const html = renderAnsi(`${RED}error${RESET}`)
    expect(html).toMatch(/<pre[^>]*>/)
    expect(html).toMatch(/<\/pre>/)
    expect(html).toContain('error')
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
})

describe('stripAnsi', () => {
  it('strips CSI controls beyond SGR while preserving printable text', () => {
    expect(stripAnsi(`${GREEN}ok${RESET}${ESC}[2K\rnext${ESC}[A`)).toBe('ok\rnext')
  })

  it('strips OSC controls while preserving surrounding text', () => {
    expect(stripAnsi(`before${ESC}]8;;https://example.com${ESC}\\link${ESC}]8;;${ESC}\\after`)).toBe('beforelinkafter')
  })
})

describe('escapeHtml', () => {
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
