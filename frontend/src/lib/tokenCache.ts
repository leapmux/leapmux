import { capMapInsertionOrder } from './mapLru'
import { recordShikiStyle, shikiStyleDecl } from './shikiStyleClass'

/**
 * Renderable token: only the fields ReadResultView / TokenizedCode / the diff
 * renderers actually use. The Shiki style itself lives in a shared CSS class
 * (see shikiStyleClass): `className` is minted at the projection boundary
 * (toCachedTokens / expandInternedTokenLines), so consumers stamp a ~14-char
 * class attribute instead of a ~50-byte inline style — and thousands of
 * same-styled spans share ONE computed style instead of each holding a
 * private inline declaration.
 */
export interface CachedToken {
  content: string
  /** Shared style class from recordShikiStyle; absent for unstyled tokens. */
  className?: string
}

/** The raw Shiki token fields the projection/merge helpers read. */
interface RawToken {
  content: string
  htmlStyle?: string | Record<string, string>
}

/**
 * Project Shiki `codeToTokens(...).tokens` into the renderable `CachedToken[][]`
 * shape, minting one shared style class per distinct htmlStyle (and injecting
 * its rule — see shikiStyleClass). Main-thread only: the token worker crosses
 * the boundary in the interned wire shape instead (internTokenLines), and the
 * client mints the classes on expansion.
 */
export function toCachedTokens(lines: ReadonlyArray<ReadonlyArray<RawToken>>): CachedToken[][] {
  return lines.map(line => line.map((t): CachedToken => {
    const className = t.htmlStyle === undefined ? undefined : recordShikiStyle(shikiStyleDecl(t.htmlStyle))
    return className === undefined ? { content: t.content } : { content: t.content, className }
  }))
}

const RE_WHITESPACE_ONLY = /^\s+$/

/** The declaration renders visibly even on whitespace (background bar). */
function hasVisibleBackground(decl: string): boolean {
  return decl.includes('background-color') || decl.includes('-bg:')
}

/** The declaration draws through/under the glyph box (underline/line-through). */
function hasTextDecoration(decl: string): boolean {
  return decl.includes('text-decoration')
}

/**
 * Shrink a line's token count without changing its rendered output:
 *
 * 1. Merge whitespace-only tokens into the FOLLOWING token (whitespace renders
 *    identically under any foreground color). Shiki does this by default — but
 *    only inside `codeToHast`; the `codeToTokens` path the token worker and the
 *    ANSI tokenizer use never gets it, which is why indented code carried one
 *    extra span per line. Skipped when the whitespace's own style paints a
 *    background (an ANSI color bar is visible ink) or either side carries a
 *    text-decoration (a merged underline would extend under the spaces), and —
 *    unlike upstream — when the receiving token paints a background (its bar
 *    must not grow leftward over the spaces).
 * 2. Merge adjacent tokens whose declarations are identical (Shiki's
 *    `mergeSameStyleTokens`, same caveat), skipping text-decorated runs to
 *    match upstream's caution.
 *
 * Concatenation is preserved: the merged line's total content equals the
 * original's, which the word-diff fragment walker depends on.
 */
export function mergeLineTokens(lines: ReadonlyArray<ReadonlyArray<RawToken>>): RawToken[][] {
  return lines.map((line) => {
    const decls = line.map(t => t.htmlStyle === undefined ? '' : shikiStyleDecl(t.htmlStyle))

    // Pass 1: fold whitespace-only runs into the next token.
    const wsMerged: RawToken[] = []
    const wsDecls: string[] = []
    let carry = ''
    for (let i = 0; i < line.length; i++) {
      const token = line[i]
      const decl = decls[i]
      const wsMergeable = RE_WHITESPACE_ONLY.test(token.content)
        && !hasVisibleBackground(decl) && !hasTextDecoration(decl)
      if (wsMergeable && i + 1 < line.length) {
        carry += token.content
        continue
      }
      if (carry !== '') {
        if (!hasVisibleBackground(decl) && !hasTextDecoration(decl)) {
          wsMerged.push({ ...token, content: carry + token.content })
          wsDecls.push(decl)
        }
        else {
          wsMerged.push({ content: carry }, token)
          wsDecls.push('', decl)
        }
        carry = ''
        continue
      }
      wsMerged.push(token)
      wsDecls.push(decl)
    }

    // Pass 2: concatenate adjacent same-declaration tokens.
    const merged: RawToken[] = []
    const mergedDecls: string[] = []
    for (let i = 0; i < wsMerged.length; i++) {
      const token = wsMerged[i]
      const decl = wsDecls[i]
      const prevDecl = mergedDecls.length > 0 ? mergedDecls[mergedDecls.length - 1] : undefined
      if (prevDecl === decl && !hasTextDecoration(decl)) {
        const prev = merged[merged.length - 1]
        merged[merged.length - 1] = { ...prev, content: prev.content + token.content }
        continue
      }
      merged.push(token)
      mergedDecls.push(decl)
    }
    return merged
  })
}

/**
 * Compact wire shape for tokenized lines crossing the worker boundary: the
 * distinct `htmlStyle` values are INTERNED into one `styles` table and each token
 * carries only its table index. A large body repeats a handful of dual-theme
 * style objects across thousands of tokens, so structured-cloning one small
 * object per DISTINCT style (instead of one per token) shrinks the per-message
 * copy. Expansion mints one shared style CLASS per table entry (shikiStyleClass),
 * so the token cache retains only a class-name string per distinct style.
 *
 * Encode (worker) and decode (client) live side by side here so the index
 * convention can't drift between the two threads. The persisted token
 * artifacts (shikiWorkerClient) store this same shape: it carries the full
 * declarations, which a later session needs to re-mint the classes.
 */
export interface InternedTokenLines {
  /** Distinct htmlStyle values, referenced by index from `lines`. */
  styles: Array<string | Record<string, string>>
  /** Lines of `[styleIndex, content]` tokens; index -1 = unstyled. */
  lines: Array<Array<[number, string]>>
}

/** Worker-side encode: intern each token's htmlStyle into the styles table. */
export function internTokenLines(lines: ReadonlyArray<ReadonlyArray<RawToken>>): InternedTokenLines {
  const styles: Array<string | Record<string, string>> = []
  const indexByKey = new Map<string, number>()
  const internedLines = lines.map(line => line.map((t): [number, string] => {
    const s = t.htmlStyle
    if (s === undefined)
      return [-1, t.content]
    // Kind-prefixed key so a STRING style can never collide with an object
    // style whose JSON happens to equal it.
    const key = typeof s === 'string' ? `s:${s}` : `o:${JSON.stringify(s)}`
    let index = indexByKey.get(key)
    if (index === undefined) {
      index = styles.length
      styles.push(s)
      indexByKey.set(key, index)
    }
    return [index, t.content]
  }))
  return { styles, lines: internedLines }
}

/**
 * Client-side decode to the `CachedToken[][]` every consumer renders: each
 * styles-table entry mints its shared class ONCE (registering the CSS rule),
 * and every token with that style shares the same class-name string.
 */
export function expandInternedTokenLines(interned: InternedTokenLines): CachedToken[][] {
  const classNames = interned.styles.map(s => recordShikiStyle(shikiStyleDecl(s)))
  return interned.lines.map(line => line.map(([styleIndex, content]): CachedToken => {
    const className = styleIndex < 0 ? undefined : classNames[styleIndex]
    return className === undefined ? { content } : { content, className }
  }))
}

// Sized above a few viewports' worth of distinct code surfaces (Read + both diff sides +
// gap contexts + Bash/JSON bodies all share this one cache) so a normal scrollback session
// keeps serving the synchronous seed on re-mount instead of re-dispatching the worker and
// re-flashing plain -- the whole point of the seed path. Smaller than the markdown cache's
// 1024 because a token array (thousands of objects for a large body) is far heavier per
// entry than a markdown HTML string.
const CACHE_MAX_SIZE = 256

const cache = new Map<string, CachedToken[][]>()

/**
 * The token identity key `${lang}\0${code}`. Single-sourced here and reused by the
 * in-flight coalescer (shikiWorkerClient) and the shared token hook (useAsyncCodeTokens)
 * so the cache lookup, the dispatch dedup, and the hook's applied-key all key on the
 * SAME string -- a divergence would silently double-tokenize or miss the cache. The NUL
 * separator can't appear in a Shiki language id, so `(lang, code)` maps to the key
 * unambiguously.
 */
export function makeKey(lang: string, code: string): string {
  return `${lang}\0${code}`
}

export function getCachedTokens(lang: string, code: string): CachedToken[][] | undefined {
  const key = makeKey(lang, code)
  const cached = cache.get(key)
  if (cached !== undefined) {
    // LRU: move to end (most recently used)
    cache.delete(key)
    cache.set(key, cached)
    return cached
  }
  return undefined
}

export function setCachedTokens(lang: string, code: string, tokens: CachedToken[][]): void {
  const key = makeKey(lang, code)
  // delete+set moves an existing key to the most-recently-used end (and inserts a fresh
  // key there); capMapInsertionOrder then drops the oldest entries past the bound.
  // Sharing the tested LRU util keeps this cache's eviction identical to the markdown
  // cache's and avoids the bespoke path's needless eviction when OVERWRITING an existing
  // key at capacity (size never grew, yet the old code dropped an unrelated live entry).
  cache.delete(key)
  cache.set(key, tokens)
  capMapInsertionOrder(cache, CACHE_MAX_SIZE)
}

/** Visible for testing: the capacity bound, and a hook to clear the shared cache. */
export const _TOKEN_CACHE_MAX_SIZE = CACHE_MAX_SIZE
export function _resetTokenCache(): void {
  cache.clear()
}
