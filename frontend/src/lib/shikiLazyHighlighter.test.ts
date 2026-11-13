import { bundledLanguages } from 'shiki/langs'
import { describe, expect, it } from 'vitest'
import { createLazyOnigurumaHighlighter } from './shikiLazyHighlighter'
import { syntaxThemePair } from './shikiThemes'

describe('createLazyOnigurumaHighlighter', () => {
  it('lazily loads a non-eager language and tokenizes with dual-theme CSS variables', async () => {
    const hl = createLazyOnigurumaHighlighter()
    expect(hl.getHighlighter()).toBeNull()

    expect(await hl.ensureLanguage('ruby')).toBe('loaded')

    const highlighter = hl.getHighlighter()
    expect(highlighter).not.toBeNull()
    expect(highlighter!.getLoadedLanguages()).toContain('ruby')
    const { tokens } = highlighter!.codeToTokens('puts 1', {
      lang: 'ruby',
      // The pair this isolate holds -- Default's, unless a test set another.
      themes: { light: syntaxThemePair().light, dark: syntaxThemePair().dark },
      defaultColor: false,
    })
    // With defaultColor:false, htmlStyle is an object of CSS-variable entries.
    const styles = tokens.flat().map(t => JSON.stringify(t.htmlStyle)).join(' ')
    expect(styles).toContain('--shiki-light')
    expect(styles).toContain('--shiki-dark')
  })

  it('returns "unsupported" (no throw) for an unknown language', async () => {
    const hl = createLazyOnigurumaHighlighter()
    expect(await hl.ensureLanguage('definitely-not-a-language')).toBe('unsupported')
    // An unknown id starts nothing, not even the highlighter.
    expect(hl.getHighlighter()).toBeNull()
  })

  it('surfaces every bundled key in getLoadedLanguages after loading it (two-registry agreement)', async () => {
    // Two code paths gate on DIFFERENT alias registries and the system assumes
    // they agree: the worker/editor PRE-LOAD path resolves a fence/extension via
    // `resolveBundledLang` (membership in `bundledLanguages` keys -- ids + the
    // alias keys Shiki bundles) and loads the grammar; the markdown RENDER path
    // (rehype-shiki) then gates each fence on
    // `highlighter.getLoadedLanguages().includes(fenceLang)`, falling back to a
    // plain `text` block on a miss. `getLoadedLanguages()` returns grammar names
    // plus only the aliases each GRAMMAR self-declares -- a different set in
    // principle from `bundledLanguages` keys. If a `bundledLanguages` alias key
    // were ever absent from the loaded grammar's self-declared aliases, that
    // fence would pre-load yet render plain. This guards the invariant
    // exhaustively: load every bundled key (via its canonical id, exactly as the
    // pre-load path does) and assert the render gate would see it. A Shiki
    // upgrade that breaks the agreement fails here instead of silently.
    const hl = createLazyOnigurumaHighlighter()
    const desynced: string[] = []
    for (const key of Object.keys(bundledLanguages)) {
      const result = await hl.ensureLanguage(key)
      if (result !== 'loaded') {
        desynced.push(`${key} (${result})`)
        continue
      }
      // The exact check rehype-shiki performs on the rendered fence language.
      if (!hl.getHighlighter()!.getLoadedLanguages().includes(key))
        desynced.push(key)
    }
    expect(desynced).toEqual([])
    // Loads every bundled grammar -- 200-odd -- serially through the Oniguruma
    // WASM engine, which takes 7-10s on a loaded machine. Vitest's 5s default
    // made this the one test in the suite that failed on CPU contention rather
    // than on the invariant it guards. Exhaustiveness is the point of the test,
    // so the budget moves rather than the coverage.
  }, 60_000)

  it('deduplicates concurrent loads of the same language (same in-flight promise)', async () => {
    const hl = createLazyOnigurumaHighlighter()
    const p1 = hl.ensureLanguage('go')
    const p2 = hl.ensureLanguage('go')
    // Both calls share the single in-flight promise rather than loading twice.
    expect(p1).toBe(p2)
    expect(await Promise.all([p1, p2])).toEqual(['loaded', 'loaded'])
    expect(hl.getHighlighter()!.getLoadedLanguages()).toContain('go')
    // A later call short-circuits via the loaded set.
    expect(await hl.ensureLanguage('go')).toBe('loaded')
  })
})
