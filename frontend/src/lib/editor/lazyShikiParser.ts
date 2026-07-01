import type { Decoration as DecorationType } from '@milkdown/prose/view'
import type { LanguageLoadResult, LazyHighlighter } from '~/lib/shikiLazyHighlighter'
import { Decoration } from '@milkdown/prose/view'
import { resolveBundledLang } from '~/lib/shikiLazyHighlighter'
import { DUAL_THEME_TOKEN_OPTIONS } from '~/lib/shikiThemes'

/** A prosemirror-highlight parser: decorations now, or a promise to re-run later. */
interface ParserOptions {
  content: string
  pos: number
  language?: string
  size: number
}

// codeToTokens with defaultColor:false (DUAL_THEME_TOKEN_OPTIONS) emits per-token
// `--shiki-light`/`--shiki-dark` CSS variables (via htmlStyle) plus a rootStyle carrying
// the `*-bg` variables, which the editor CSS (MarkdownEditor.css.ts `.ProseMirror pre
// .shiki`) maps to the theme. Same contract as the stock @milkdown/plugin-highlight/shiki
// adapter this replaces.

function stringifyTokenStyle(style: string | Record<string, string>): string {
  return typeof style === 'string'
    ? style
    : Object.entries(style).map(([key, value]) => `${key}:${value}`).join(';')
}

/**
 * A prosemirror-highlight parser backed by the async, lazily-loading Oniguruma
 * highlighter (replacing the stock `@milkdown/plugin-highlight/shiki` adapter,
 * which assumes an eager synchronous highlighter).
 *
 * When the highlighter or the block's grammar isn't ready yet, it returns a
 * promise; `createHighlightPlugin` re-runs the parser once the promise resolves
 * (it removes the block from its cache and dispatches a refresh), so the block
 * paints plain for a beat and then highlights.
 *
 * A grammar load can fail transiently (a chunk-import network hiccup), and
 * `ensureLanguage` clears its in-flight entry on failure so a retry can re-load.
 * We therefore retry a bounded number of times instead of latching to plain on
 * the first failure: a transient failure recovers on a later re-run, while a
 * grammar that genuinely never loads is given up on after `MAX_LANG_LOAD_RETRIES`
 * attempts (the parser then returns `[]`, which `createHighlightPlugin` caches,
 * ending the resolve->re-run loop instead of re-awaiting forever).
 */
const MAX_LANG_LOAD_RETRIES = 3

export function createLazyShikiParser(
  lazyHl: LazyHighlighter,
): (opts: ParserOptions) => DecorationType[] | Promise<void> {
  // Per-language count of failed load attempts; a language that exhausts the
  // retry budget renders plain for the rest of this editor mount.
  const failures = new Map<string, number>()
  // One counted load attempt per language in flight at a time. A document can hold
  // several code blocks of the SAME language, and prosemirror-highlight invokes this
  // parser once PER block in a single decoration pass; without this de-dup each block
  // would attach its own `.then` to the single (de-duplicated) `ensureLanguage` load
  // and bump `failures` on a shared failure -- so N>=MAX_LANG_LOAD_RETRIES same-language
  // blocks would exhaust the whole retry budget on ONE transient hiccup, latching them
  // all to plain. Counting once per actual load attempt keeps the budget meaningful.
  const pendingLoads = new Map<string, Promise<LanguageLoadResult>>()
  return ({ content, language, pos, size }) => {
    const lang = language ? resolveBundledLang(language) : undefined
    // Unknown/absent language: plain.
    if (!lang)
      return []

    const highlighter = lazyHl.getHighlighter()
    if (!highlighter || !lazyHl.isLanguageLoaded(lang)) {
      // Grammar not loaded yet. The retry-budget wedge is checked HERE (not before the
      // loaded check above) because the highlighter -- and thus its `loaded` set -- is
      // SHARED across every editor mount, while `failures` is per-parser. A language this
      // mount gave up loading may since have been loaded by another mount; once it is
      // loaded the block above tokenizes it, so the budget must only suppress further load
      // ATTEMPTS, never a grammar that is now actually available.
      if ((failures.get(lang) ?? 0) >= MAX_LANG_LOAD_RETRIES)
        return []
      // Kick off init + grammar load; resolving triggers a re-run of this parser.
      // Share one load + one failure increment across every block requesting this
      // language in the same pass (see pendingLoads above).
      let load = pendingLoads.get(lang)
      if (!load) {
        load = lazyHl.ensureLanguage(lang)
        pendingLoads.set(lang, load)
        void load.then((result) => {
          // `lang` is already a resolveBundledLang result, so 'unsupported' can't occur
          // here; only a transient 'failed' counts against the retry budget.
          if (result !== 'loaded')
            failures.set(lang, (failures.get(lang) ?? 0) + 1)
          pendingLoads.delete(lang)
        })
      }
      return load.then(() => {})
    }

    try {
      const { tokens, rootStyle } = highlighter.codeToTokens(content, { lang, ...DUAL_THEME_TOKEN_OPTIONS })
      const decorations: DecorationType[] = []
      if (rootStyle)
        decorations.push(Decoration.node(pos, pos + size, { style: rootStyle }))
      let from = pos + 1
      for (const line of tokens) {
        for (const token of line) {
          const to = from + token.content.length
          decorations.push(Decoration.inline(from, to, {
            style: stringifyTokenStyle(token.htmlStyle ?? ''),
            class: 'shiki',
          }))
          from = to
        }
        // Account for the newline between rendered lines.
        from += 1
      }
      return decorations
    }
    catch (error) {
      // A LOADED grammar threw at tokenize time (an engine/grammar-version mismatch, a
      // regex-engine blowup on pathological input). Degrade THIS block to plain by
      // returning []; without the catch the throw escapes into prosemirror-highlight's
      // calculateDecoration, whose try/catch wraps the entire code-block loop -- so one
      // bad block would drop highlighting for every block in the pass. Dev-warn surfaces
      // the regression (mirrors markdownProcessor's onError); production stays silent.
      if (import.meta.env.DEV)
        console.warn('[lazyShikiParser] Shiki failed to tokenize a code block:', error)
      return []
    }
  }
}
