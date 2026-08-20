import type { SyntaxThemePair } from './syntaxThemes'
import { syntaxPairFor } from './syntaxThemes'

/**
 * The syntax theme pair every Shiki highlighter in this isolate registers, and
 * the `codeToTokens` options every call site shares.
 *
 * MUTABLE, and that is the whole design. A tokenized span carries baked colours,
 * so a syntax theme cannot be swapped in CSS the way the UI palette can -- the
 * only way to change it is to tokenize again with a different theme. So the pair
 * is module state that the app sets from the preference, and every cache that
 * holds tokenized output folds `syntaxThemeKey()` into its key so a change
 * orphans the old entries instead of serving them under the new theme.
 *
 * This module is imported by BOTH web workers and by the main thread, and each
 * is a separate isolate with its own copy of this state. Nothing synchronizes
 * them: instead every request carries the pair it wants (see
 * `shikiWorkerClient` / `markdownWorkerClient`), and the receiving isolate
 * applies it before rendering. A handshake would leave a window where a request
 * issued under the old theme is answered under the new one.
 *
 * The CSS contract does NOT change with the theme. Shiki names its dual-theme
 * variables after the KEYS of the `themes` object, not after the theme names, so
 * `--shiki-light` / `--shiki-dark` mean the same thing under every pair and
 * `shikiTokenColors.css.ts` needs no per-theme rule.
 */

let currentPair: SyntaxThemePair = syntaxPairFor('default')

/** The pair this isolate currently tokenizes with. */
export function syntaxThemePair(): SyntaxThemePair {
  return currentPair
}

/**
 * Point this isolate at `pair`. Returns whether it actually changed, so callers
 * can skip the cache invalidation and re-render when it did not.
 */
export function setSyntaxThemePair(pair: SyntaxThemePair): boolean {
  if (sameSyntaxPair(currentPair, pair))
    return false
  currentPair = pair
  return true
}

/**
 * Whether two pairs name the same two themes.
 *
 * One spelling of the comparison, because it decides four separate things --
 * whether a set is a change, whether a captured pair is still current, and
 * whether either markdown processor must be rebuilt -- and a pair that gains a
 * third field must move all four at once.
 */
export function sameSyntaxPair(a: SyntaxThemePair, b: SyntaxThemePair): boolean {
  return a.light === b.light && a.dark === b.dark
}

/**
 * A one-slot cache whose entry is valid only for the pair it was built under.
 *
 * FOR A VALUE THAT BAKES THE PAIR IN AT CONSTRUCTION, which is what
 * `createMarkdownProcessor` does: the Shiki plugin it wires up holds the two
 * theme names, so a processor cached with no key goes on tokenizing in those
 * colours for the life of the isolate, however many times the user changes the
 * syntax theme afterwards. Both markdown paths hit that, separately, and each
 * grew its own pair of module-level `let`s and its own three-clause staleness
 * test to answer it -- the shape this replaces.
 *
 * `build` is passed per call rather than per cache, so a caller can close over
 * inputs that are not the pair (the worker's highlighter arrives with the
 * request). It runs on a miss only.
 *
 * ONE SLOT, not a map keyed by pair: a rebuild is cheap next to a render, it
 * happens once per theme change rather than once per body, and a map would hold
 * a whole processor for every theme the user ever sampled.
 *
 * CALL IT AT THE POINT OF USE, with no await between the call and the work.
 * Both callers suspend between a request arriving and its render, so resolving
 * the value before those awaits let a second request on another pair replace it
 * in the gap -- and the first request rendered in the second's colours.
 */
export function createPairKeyedCache<T>(): (pair: SyntaxThemePair, build: () => T) => T {
  let value: T | null = null
  let builtFor: SyntaxThemePair | null = null
  return (pair, build) => {
    if (value === null || builtFor === null || !sameSyntaxPair(builtFor, pair)) {
      value = build()
      builtFor = pair
    }
    return value
  }
}

/**
 * Whether `pair` is still the pair this isolate tokenizes with.
 *
 * ASK THIS BEFORE EVERY WRITE OF TOKENIZED OUTPUT that was produced under a
 * captured pair -- a worker reply, and an IndexedDB artifact read alike. Both
 * resolve asynchronously, so the user can choose another theme in the window,
 * and the in-memory caches key on the source alone: writing an abandoned
 * theme's output restores exactly what the invalidator cleared, and no later
 * read re-dispatches, because a cache hit never does.
 *
 * It takes the pair rather than a key string, so a caller cannot build the key
 * in a spelling that differs from `syntaxThemeKey`'s.
 */
export function themeStillCurrent(pair: SyntaxThemePair): boolean {
  return sameSyntaxPair(currentPair, pair)
}

/**
 * A stable string for the current pair, for cache keys and artifact namespaces.
 *
 * Every store that holds TOKENIZED output must fold this in. The in-memory
 * caches key on it directly; the IndexedDB namespaces embed it, so entries
 * written under one theme are orphaned rather than served under another.
 */
export function syntaxThemeKey(): string {
  return `${currentPair.light},${currentPair.dark}`
}

/**
 * The dual-theme `codeToTokens`/`codeToHast`/`codeToHtml` options every Shiki
 * call site shares: the current pair with `defaultColor` off, so Shiki emits
 * per-token `--shiki-light`/`--shiki-dark` CSS variables instead of a single
 * baked-in colour.
 *
 * This contract is load-bearing: the CSS that themes Shiki output (the
 * `pre.shiki` / `[data-shiki-token]` rules in messageStyles / toolStyles /
 * markdownContent) keys off exactly these variables. Single-sourcing it here
 * keeps the `defaultColor` flag from drifting between the token worker, the
 * markdown pipeline, the editor parser, the ANSI renderer, and the Read tool
 * view -- a mismatch in one path would silently theme that surface differently.
 *
 * A FUNCTION, not a constant: it reads the current pair, so a call site that
 * captured it once would keep tokenizing with the theme that was current when
 * its module first evaluated.
 *
 * PASS `pair` WHEREVER ONE IS IN HAND. Reading the module state is right only
 * for a synchronous main-thread call that has no request of its own. A call
 * that serves a request must name that request's pair, because the module state
 * can move under an `await`: both workers accept a second message while the
 * first is suspended on a grammar or theme import, and the second's pair would
 * otherwise decide the first's colours. An argument cannot be raced.
 */
export function dualThemeTokenOptions(pair: SyntaxThemePair = currentPair): {
  themes: { light: string, dark: string }
  defaultColor: false
} {
  return { themes: { light: pair.light, dark: pair.dark }, defaultColor: false }
}
