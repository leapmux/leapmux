import type { Decoration as DecorationType } from '@milkdown/prose/view'
import type { LanguageLoadResult, LazyHighlighter } from '~/lib/shikiLazyHighlighter'
import { Decoration } from '@milkdown/prose/view'
import { resolveBundledLang } from '~/lib/shikiLazyHighlighter'
import { dualThemeTokenOptions, syntaxThemePair } from '~/lib/shikiThemes'

/** A prosemirror-highlight parser: decorations now, or a promise to re-run later. */
interface ParserOptions {
  content: string
  pos: number
  language?: string
  size: number
}

// codeToTokens with defaultColor:false (dualThemeTokenOptions) emits per-token
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
  // One theme registration in flight at a time. Every block in a pass wants the
  // same pair, so they share the load rather than each firing their own.
  let pendingThemeLoad: Promise<void> | null = null
  // Per-pair count of failed registration attempts, and the budget the grammar
  // branch already has. `ensureThemes` REJECTS when a lazy theme chunk fails to
  // import -- `loadSyntaxTheme` deletes the rejected entry precisely so a later
  // call retries -- and a rejected promise returned to `prosemirror-highlight`
  // is logged and dropped WITHOUT a refresh, leaving the block uncached. So
  // every later keystroke re-ran the parser, saw the pair still unregistered,
  // and fired another failing chunk import with another console error: one per
  // code block, per edit, for the rest of the session.
  const themeFailures = new Map<string, number>()
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

    // The pair this block must paint with, captured once so the registration
    // check and the tokenize below cannot disagree.
    const pair = syntaxThemePair()
    if (!lazyHl.areThemesLoaded(pair)) {
      // The budget, checked AFTER `areThemesLoaded` for the reason the grammar
      // branch checks its own after `isLanguageLoaded`: the highlighter is
      // shared across editor mounts, so a pair this parser gave up on may since
      // have been registered by another. The budget suppresses further
      // ATTEMPTS, never a pair that is now actually available.
      const pairKey = `${pair.light},${pair.dark}`
      if ((themeFailures.get(pairKey) ?? 0) >= MAX_LANG_LOAD_RETRIES)
        return []
      // Same contract as the grammar branch above: return the load, and
      // resolving re-runs this parser. The editor's highlighter is a session-long
      // singleton that registers only the pair live at its FIRST use, and nothing
      // else re-registers it -- `setSyntaxTheme` targets the separate synchronous
      // main-thread instance. Without this, the first syntax-theme change of a
      // session made every `codeToTokens` here name a theme this instance never
      // loaded; Shiki threw, the catch below swallowed it, and every fenced block
      // in the composer rendered as plain text until a full page reload.
      let load = pendingThemeLoad
      if (!load) {
        // NORMALIZED to a resolution, like `ensureLanguage`'s `'failed'`. A
        // rejection here reaches `prosemirror-highlight`, which logs it and
        // does NOT refresh -- so the block stays uncached and the next
        // keystroke tries again forever. Resolving instead lets the parser
        // re-run, find the budget spent, and return `[]`, which the plugin
        // caches: the block renders plain and the loop ends.
        load = lazyHl.ensureThemes(pair)
          .catch(() => {
            themeFailures.set(pairKey, (themeFailures.get(pairKey) ?? 0) + 1)
          })
          .finally(() => {
            pendingThemeLoad = null
          })
        pendingThemeLoad = load
      }
      return load
    }

    try {
      const { tokens, rootStyle } = highlighter.codeToTokens(content, { lang, ...dualThemeTokenOptions(pair) })
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
