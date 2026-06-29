import { capMapInsertionOrder } from './mapLru'

/** Serializable token: only the fields ReadResultView actually uses. */
export interface CachedToken {
  content: string
  htmlStyle?: string | Record<string, string>
}

/**
 * Project Shiki `codeToTokens(...).tokens` into the serializable `CachedToken[][]`
 * wire shape (content + htmlStyle only), dropping the fields ReadResultView /
 * TokenizedCode don't use so the result survives structured-clone across the worker
 * boundary. Shared by the token worker and the main-thread ANSI path so the two
 * projections can't drift.
 */
export function toCachedTokens(
  lines: ReadonlyArray<ReadonlyArray<{ content: string, htmlStyle?: string | Record<string, string> }>>,
): CachedToken[][] {
  return lines.map(line => line.map(t => ({ content: t.content, htmlStyle: t.htmlStyle })))
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
