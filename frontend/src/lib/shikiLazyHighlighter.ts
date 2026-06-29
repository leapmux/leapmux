import type { BundledLanguage } from 'shiki'
import type { HighlighterCore } from 'shiki/core'
import { createHighlighterCore } from 'shiki/core'
import { createOnigurumaEngine } from 'shiki/engine/oniguruma'
import { bundledLanguages } from 'shiki/langs'
import { transparentBgThemes } from './shikiThemes'

/**
 * Outcome of a lazy grammar load, so callers can tell a PERMANENT miss apart from
 * a TRANSIENT failure and pick their own recovery:
 *   - `'loaded'`     -- the grammar is available to tokenize.
 *   - `'unsupported'`-- Shiki bundles no grammar for this id; rendering plain is
 *                       correct and final (never retry).
 *   - `'failed'`     -- a real bundled grammar whose chunk import / compile threw
 *                       (e.g. a network hiccup); a later retry may succeed, so a
 *                       caller must NOT cache this outcome as permanent.
 *
 * Before this distinction all three collapsed to `false`, so the markdown worker
 * cached a transiently-failed render as plain forever while the token worker and
 * editor parser recovered -- three divergent behaviors from one boolean. Callers
 * now share one policy off this result.
 */
export type LanguageLoadResult = 'loaded' | 'unsupported' | 'failed'

/**
 * A lazily-initialized Shiki highlighter backed by the Oniguruma WASM engine.
 *
 * Unlike the synchronous main-thread `shikiHighlighter` (JS regex engine, fixed
 * 20-language set), this one:
 *   - uses Oniguruma (Shiki's reference engine: faster on complex grammars and
 *     fully accurate -- no JS-engine lookbehind degradation), which requires an
 *     async WASM init, hence everything here is promise-based; and
 *   - loads grammars on demand from Shiki's ~332 bundled languages, caching each
 *     so a language is compiled at most once per instance.
 *
 * Each isolate that needs highlighting creates its own instance (the two Web
 * Workers are separate threads with no shared memory; the editor runs one on the
 * main thread). The inlined Oniguruma WASM (`shiki/wasm`) is compiled once per
 * instance on first use.
 */
export interface LazyHighlighter {
  /** Resolve once the highlighter (incl. its WASM engine) has initialized. */
  ensureReady: () => Promise<HighlighterCore>
  /**
   * Ensure the grammar for `lang` (id or alias) is loaded. Resolves `'loaded'`
   * when the language is available to tokenize, `'unsupported'` for an unknown id
   * (permanent -- render plain), or `'failed'` for a transient load failure
   * (retryable). Idempotent: a loaded language and an in-flight load are both
   * deduplicated; a failed load drops out of the in-flight set so a later call
   * re-attempts.
   */
  ensureLanguage: (lang: string) => Promise<LanguageLoadResult>
  /** Whether `lang` (id or alias) is already loaded -- a synchronous check. */
  isLanguageLoaded: (lang: string) => boolean
  /** The initialized highlighter, or null before `ensureReady` resolves. */
  getHighlighter: () => HighlighterCore | null
}

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
  return Object.hasOwn(bundledLanguages, key) ? (key as BundledLanguage) : undefined
}

export function createLazyOnigurumaHighlighter(): LazyHighlighter {
  let highlighter: HighlighterCore | null = null
  let readyPromise: Promise<HighlighterCore> | null = null
  // Canonical ids whose grammar is loaded, and the in-flight loads (dedup).
  const loaded = new Set<BundledLanguage>()
  const inFlight = new Map<BundledLanguage, Promise<LanguageLoadResult>>()

  function ensureReady(): Promise<HighlighterCore> {
    if (highlighter)
      return Promise.resolve(highlighter)
    if (!readyPromise) {
      readyPromise = createHighlighterCore({
        themes: transparentBgThemes,
        // Start empty; grammars load lazily via ensureLanguage.
        langs: [],
        engine: createOnigurumaEngine(import('shiki/wasm')),
      }).then((h) => {
        highlighter = h
        return h
      })
      // If init REJECTS (WASM/engine load failure), drop the cached promise so a
      // later call retries instead of re-awaiting the same rejection forever.
      void readyPromise.catch(() => {
        readyPromise = null
      })
    }
    return readyPromise
  }

  function ensureLanguage(lang: string): Promise<LanguageLoadResult> {
    const canonical = resolveBundledLang(lang)
    if (!canonical)
      return Promise.resolve('unsupported')
    if (loaded.has(canonical))
      return Promise.resolve('loaded')
    const existing = inFlight.get(canonical)
    if (existing)
      return existing
    const promise = ensureReady()
      .then(h => h.loadLanguage(bundledLanguages[canonical]))
      .then((): LanguageLoadResult => {
        loaded.add(canonical)
        inFlight.delete(canonical)
        return 'loaded'
      })
      .catch((): LanguageLoadResult => {
        // A failed load drops out of inFlight so a later request can retry, and
        // reports 'failed' (not 'unsupported') so callers know it is transient.
        inFlight.delete(canonical)
        return 'failed'
      })
    inFlight.set(canonical, promise)
    return promise
  }

  return {
    ensureReady,
    ensureLanguage,
    isLanguageLoaded: (lang) => {
      const canonical = resolveBundledLang(lang)
      return canonical ? loaded.has(canonical) : false
    },
    getHighlighter: () => highlighter,
  }
}
