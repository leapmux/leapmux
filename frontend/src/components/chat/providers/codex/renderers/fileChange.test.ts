import { afterEach, describe, expect, it } from 'vitest'
import { __unifiedDiffCacheForTest } from '../../../diff/unifiedDiffParser'

const { cache, limit, parse } = __unifiedDiffCacheForTest

function makeDiff(seed: number): string {
  // Each seed produces a distinct unified diff with one hunk.
  return `@@ -1,1 +1,1 @@\n-old line ${seed}\n+new line ${seed}\n`
}

describe('parseCodexUnifiedDiffCached', () => {
  afterEach(() => {
    cache.clear()
  })

  it('returns reference-equal output on cache hit', () => {
    const diff = makeDiff(1)
    const a = parse(diff)
    const b = parse(diff)
    expect(a).not.toBeNull()
    expect(b).toBe(a)
  })

  it('keeps the cache size at or below the configured limit', () => {
    for (let i = 0; i < limit + 10; i++)
      parse(makeDiff(i))
    expect(cache.size).toBeLessThanOrEqual(limit)
  })

  it('evicts least-recently-used entries before more recent ones', () => {
    // Fill the cache to exactly the limit.
    for (let i = 0; i < limit; i++)
      parse(makeDiff(i))
    expect(cache.size).toBe(limit)

    // Touch entry 0 — it should now be the most-recently-used.
    parse(makeDiff(0))

    // Insert one more entry, forcing one eviction.
    parse(makeDiff(limit))

    expect(cache.has(makeDiff(0))).toBe(true) // touched, kept
    expect(cache.has(makeDiff(1))).toBe(false) // oldest after touch, evicted
    expect(cache.has(makeDiff(limit))).toBe(true) // just inserted
    expect(cache.size).toBe(limit)
  })

  it('returns null and caches null for empty / whitespace diffs', () => {
    expect(parse('')).toBeNull()
    expect(parse('   \n\n   ')).toBeNull()
    // Calling twice doesn't grow the cache for the empty short-circuit.
    parse('   \n\n   ')
    // Whitespace-only diffs do reach the parser and cache a null result;
    // the empty-string short-circuit before lookup means '' itself never
    // takes a cache slot.
    expect(cache.has('')).toBe(false)
  })
})
