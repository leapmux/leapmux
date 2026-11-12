import { describe, expect, it } from 'vitest'
import { DEFAULT_THEME_ID } from '~/styles/themes'
import { shikiHighlighter } from './shikiSyncHighlighter'
import { syntaxPairFor } from './syntaxThemes'

const ESC = '\x1B'

/**
 * The synchronous highlighter must boot already holding the pair the app
 * starts on.
 *
 * It cannot await a chunk, so that one pair is imported statically. Nothing in
 * the type system ties that import to `SYNTAX_THEMES.default`, and the two DID
 * drift apart once: pointing Default at another pair while the static import
 * stayed behind leaves every synchronous call site -- `renderAnsi` and
 * `ansiSyncTokenize` -- naming a theme Shiki has not loaded, which throws on the
 * first highlight of the session.
 */
describe('shikiHighlighter', () => {
  it('registers the pair the theme catalogue names for Default', () => {
    const pair = syntaxPairFor(DEFAULT_THEME_ID)
    const loaded = shikiHighlighter.getLoadedThemes()
    expect(loaded).toContain(pair.light)
    expect(loaded).toContain(pair.dark)
  })

  it('colours ANSI output under that pair without registering anything', () => {
    // The assertion that would actually have caught the drift: naming an
    // unregistered theme throws rather than degrading.
    const pair = syntaxPairFor(DEFAULT_THEME_ID)
    const { tokens } = shikiHighlighter.codeToTokens(`${ESC}[31mred${ESC}[0m plain`, {
      lang: 'ansi',
      themes: { light: pair.light, dark: pair.dark },
      defaultColor: false,
    })
    const red = tokens.flat().find(token => token.content === 'red')
    const plain = tokens.flat().find(token => token.content.includes('plain'))
    expect(red?.htmlStyle).toBeDefined()
    expect(red?.htmlStyle).not.toEqual(plain?.htmlStyle)
  })

  it('holds no grammar, so the browser pays for none at load', () => {
    expect(shikiHighlighter.getLoadedLanguages()).toEqual([])
    const theme = syntaxPairFor(DEFAULT_THEME_ID).light
    expect(() => shikiHighlighter.codeToTokens('const a = 1', { lang: 'typescript', theme })).toThrow(/^Language `typescript` not found/)
  })
})
