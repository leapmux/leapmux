import { bundledLanguages } from 'shiki/langs'
import { describe, expect, it } from 'vitest'
import { createLazyOnigurumaHighlighter, resolveBundledLang } from './shikiLazyHighlighter'

describe('resolveBundledLang', () => {
  it('resolves canonical bundled ids', () => {
    expect(resolveBundledLang('typescript')).toBe('typescript')
    expect(resolveBundledLang('ruby')).toBe('ruby')
  })

  it('resolves alias keys that Shiki bundles directly', () => {
    // Shiki ships aliases as their own bundledLanguages keys.
    expect(resolveBundledLang('ts')).toBe('ts')
    expect(resolveBundledLang('c++')).toBe('c++')
  })

  it('returns undefined for unknown languages and built-ins without a grammar', () => {
    expect(resolveBundledLang('definitely-not-a-language')).toBeUndefined()
    // `ansi` is a Shiki built-in tokenized on the main thread, not a bundled grammar.
    expect(resolveBundledLang('ansi')).toBeUndefined()
  })

  it('does not treat Object.prototype keys as bundled languages', () => {
    // bundledLanguages is a plain `{...base, ...alias}` object, so a naive `key in obj`
    // check would match inherited prototype members -- then loadLanguage(bundledLanguages[key])
    // gets the Object constructor / prototype and throws. These keys reach the resolver from
    // user input (fence languages, code-block attrs, file extensions), so they must be rejected.
    for (const key of ['constructor', '__proto__', 'toString', 'valueOf', 'hasOwnProperty', 'isPrototypeOf'])
      expect(resolveBundledLang(key), key).toBeUndefined()
  })

  it('resolves case-insensitively to the lowercase bundled key', () => {
    // Shiki bundles every id/alias lowercase; a mixed-case id (e.g. a `JSON` code-block
    // attribute or a `TS` fence) should still resolve instead of falling through to plain.
    expect(resolveBundledLang('TypeScript')).toBe('typescript')
    expect(resolveBundledLang('JSON')).toBe('json')
    expect(resolveBundledLang('TS')).toBe('ts')
  })
})

describe('createLazyOnigurumaHighlighter', () => {
  it('lazily loads a non-eager language and tokenizes with dual-theme CSS variables', async () => {
    const hl = createLazyOnigurumaHighlighter()
    expect(hl.isLanguageLoaded('ruby')).toBe(false)
    expect(hl.getHighlighter()).toBeNull()

    expect(await hl.ensureLanguage('ruby')).toBe('loaded')
    expect(hl.isLanguageLoaded('ruby')).toBe(true)

    const highlighter = hl.getHighlighter()
    expect(highlighter).not.toBeNull()
    const { tokens } = highlighter!.codeToTokens('puts 1', {
      lang: 'ruby',
      themes: { light: 'github-light', dark: 'github-dark' },
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
    expect(hl.isLanguageLoaded('definitely-not-a-language')).toBe(false)
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
  })

  it('deduplicates concurrent loads of the same language (same in-flight promise)', async () => {
    const hl = createLazyOnigurumaHighlighter()
    const p1 = hl.ensureLanguage('go')
    const p2 = hl.ensureLanguage('go')
    // Both calls share the single in-flight promise rather than loading twice.
    expect(p1).toBe(p2)
    expect(await Promise.all([p1, p2])).toEqual(['loaded', 'loaded'])
    expect(hl.isLanguageLoaded('go')).toBe(true)
    // A later call short-circuits via the loaded set.
    expect(await hl.ensureLanguage('go')).toBe('loaded')
  })
})
