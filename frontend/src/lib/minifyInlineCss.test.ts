import { describe, expect, it, vi } from 'vitest'
import { bootSplashDocumentCss } from '~/lib/bootSplashTheme'
import { minifyInlineCss } from '~/lib/minifyInlineCss'

describe('minifyInlineCss', () => {
  it('strips comments and shrinks readable splash CSS', () => {
    const source = bootSplashDocumentCss()
    const minified = minifyInlineCss(source)

    expect(minified.length).toBeLessThan(source.length)
    expect(minified).not.toContain('/*')
    expect(minified).toContain('html,body,#app')
    expect(minified).toContain('boot-splash-enter')
    expect(minified).toContain('@supports')
  })

  it('preserves selectors the splash first paint needs', () => {
    const minified = minifyInlineCss(bootSplashDocumentCss())

    expect(minified).toContain('#boot-splash')
    // lightningcss drops quotes around simple attribute values.
    expect(minified).toContain('[data-testid=boot-splash]')
    expect(minified).toContain('color-scheme:light')
    expect(minified).toContain('color-scheme:dark')
  })

  // The document ships the MINIFIED string, and `calc()` is the one place
  // where whitespace carries meaning: `calc(a - b)` without the spaces around
  // the operator is invalid, so the whole declaration would be dropped and the
  // Solid splash would fall back to no floor at all. The geometry specs
  // measure the readable source, so nothing else covers the shipped bytes.
  it('keeps the calc operator spacing the splash floor depends on', () => {
    const minified = minifyInlineCss(bootSplashDocumentCss())

    // The spaces around `-` are the assertion. Comma spacing is lightningcss's
    // own business, so the pattern tolerates it either way.
    expect(minified).toMatch(
      /min-height:calc\(var\(--vvh,\s?100dvh\) - env\(safe-area-inset-top,\s?0px\)\)/,
    )
  })

  it('accepts empty input', () => {
    expect(minifyInlineCss('')).toBe('')
  })

  it('is stable on already-compact CSS', () => {
    const compact = 'html,body{margin:0}#boot-splash{color:red}'
    const once = minifyInlineCss(compact)
    expect(minifyInlineCss(once)).toBe(once)
    expect(once).toContain('#boot-splash')
  })

  it('returns the source when lightningcss rejects the input', async () => {
    vi.resetModules()
    vi.doMock('lightningcss', () => ({
      transform: () => {
        throw new Error('parse failed')
      },
    }))
    const { minifyInlineCss: minify } = await import('~/lib/minifyInlineCss')
    expect(minify('not { valid')).toBe('not { valid')
    vi.doUnmock('lightningcss')
    vi.resetModules()
  })
})
