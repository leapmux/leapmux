import type { BundledLanguage } from 'shiki'
import { bundledLanguages } from 'shiki/langs'

// This lookup lives apart from `shikiLazyHighlighter`, which imports the
// Oniguruma engine that only the two Web Workers create. The composer on the
// main thread resolves a language through this module and imports no engine.

/**
 * Resolve a language id or alias to a Shiki bundled-language key, or undefined
 * when Shiki ships no grammar for it. Shiki's `bundledLanguages` keys ALREADY
 * include aliases (e.g. `ts`, `c++`, `rb`, `kt`), so a single membership check
 * covers both canonical ids and aliases.
 *
 * `Object.hasOwn` (not `key in obj`): `bundledLanguages` is a plain `{...}` object,
 * so `in` would match inherited `Object.prototype` members -- `'constructor'`,
 * `'toString'`, `'__proto__'`, etc. -- and these reach here from user input (fence
 * languages, code-block attributes, file extensions). A false match would then feed
 * the `Object` constructor / prototype into `loadLanguage`, which throws. The id is
 * lower-cased first because every bundled key/alias is lowercase, so a mixed-case id
 * (a `JSON` code-block attribute, a `TS` fence) still resolves instead of falling
 * through to plain.
 */
export function resolveBundledLang(lang: string): BundledLanguage | undefined {
  const key = lang.toLowerCase()
  return Object.hasOwn(bundledLanguages, key)
    ? (key as BundledLanguage)
    : undefined
}
