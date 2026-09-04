import { describe, expect, it } from 'vitest'
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

  it('accepts empty input', () => {
    expect(minifyInlineCss('')).toBe('')
  })

  it('is stable on already-compact CSS', () => {
    const compact = 'html,body{margin:0}#boot-splash{color:red}'
    const once = minifyInlineCss(compact)
    expect(minifyInlineCss(once)).toBe(once)
    expect(once).toContain('#boot-splash')
  })
})
