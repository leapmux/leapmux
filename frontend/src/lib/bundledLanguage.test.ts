import { describe, expect, it } from 'vitest'
import { resolveBundledLang } from './bundledLanguage'

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
    for (const key of [
      'constructor',
      '__proto__',
      'toString',
      'valueOf',
      'hasOwnProperty',
      'isPrototypeOf',
    ])
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
