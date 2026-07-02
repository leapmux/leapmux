import { diffWordsWithSpace } from 'diff'
import { capMapInsertionOrder } from '~/lib/mapLru'

/** One word-diff segment, as the `diff` package emits it. */
export interface WordDiffPart {
  value: string
  added?: boolean
  removed?: boolean
}

/**
 * Sized for the paired lines of a few visible diffs. Entries are small
 * (segment arrays over single lines), so this is bytes-per-entry cheap; the
 * point is coverage across the renders that would otherwise recompute — a
 * diff row renders its word-diffs on the hidden premeasure mount, again on
 * the visible mount, again when worker tokens arrive, and again on every
 * unified/split toggle.
 */
const CACHE_MAX_SIZE = 512

const cache = new Map<string, WordDiffPart[]>()

/**
 * Word-level diff of one paired (removed, added) line, memoized.
 *
 * diffWordsWithSpace (not diffWords) is deliberate: it preserves whitespace
 * runs as separate segments. diffWords ignores whitespace during comparison
 * and attaches it to adjacent word tokens from an arbitrary side, which
 * corrupts leading indentation when the two lines differ in indentation
 * level.
 *
 * The removed-side and added-side renderers both need the same parts, so a
 * paired line hits this cache on its second call even within one render;
 * across renders the cache absorbs the premeasure/visible double mount and
 * view toggles. Callers must treat the returned array as immutable — it is
 * shared between both sides and across renders.
 */
export function pairedWordDiff(oldLine: string, newLine: string): WordDiffPart[] {
  // The length prefix keeps the key injective even if a line contained the
  // separator character.
  const key = `${oldLine.length}\0${oldLine}\0${newLine}`
  const cached = cache.get(key)
  if (cached !== undefined) {
    // Re-set so the entry becomes most-recently-used (Map insertion order).
    cache.delete(key)
    cache.set(key, cached)
    return cached
  }
  const parts = diffWordsWithSpace(oldLine, newLine)
  cache.set(key, parts)
  if (cache.size > CACHE_MAX_SIZE)
    capMapInsertionOrder(cache, CACHE_MAX_SIZE)
  return parts
}

/** Test-only: reset the module cache so tests don't observe each other. */
export function clearWordDiffCacheForTest(): void {
  cache.clear()
}
