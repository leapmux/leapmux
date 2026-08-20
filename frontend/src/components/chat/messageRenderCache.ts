import { lruGet, lruSet } from '~/lib/mapLru'
import { fnv1a32Hex } from '~/lib/stringDigest'

export interface MessageRenderCache {
  get: <T>(key: string) => T | undefined
  set: <T>(key: string, value: T) => T
  getOrCreate: <T>(key: string, compute: () => T) => T
}

export interface MessageRenderCacheStore {
  forRow: (rowVersionKey: string) => MessageRenderCache
  prune: (liveRowVersionKeys: Iterable<string>) => void
  /**
   * Drop every cached value, keeping no row.
   *
   * For a change that invalidates output across EVERY row at once -- a syntax
   * theme change, whose baked token colours every cached body carries. `prune`
   * cannot do it: it drops whole rows that left the list, and these rows are
   * still live. The callers fold the theme generation into their KEY, so
   * without this the old generation's entries were merely orphaned inside each
   * live row's map, which nothing bounds by key count -- trying a dozen themes
   * kept a dozen copies of every visible row's HTML for the life of the tab.
   */
  clear: () => void
  size: () => number
}

const DEFAULT_MAX_RENDER_CACHE_ROWS = 512

export function createMessageRenderCacheStore(maxRows = DEFAULT_MAX_RENDER_CACHE_ROWS): MessageRenderCacheStore {
  const rowCaches = new Map<string, Map<string, unknown>>()

  const touch = (rowVersionKey: string): Map<string, unknown> => {
    // Shared LRU (mapLru): a hit re-fronts to the MRU end; a miss inserts a fresh
    // per-row cache and sheds the insertion-order-oldest rows past `maxRows`.
    const existing = lruGet(rowCaches, rowVersionKey)
    if (existing !== undefined)
      return existing
    const cache = new Map<string, unknown>()
    lruSet(rowCaches, rowVersionKey, cache, maxRows)
    return cache
  }

  return {
    forRow(rowVersionKey) {
      const cache = touch(rowVersionKey)
      return {
        get<T>(key: string): T | undefined {
          return cache.get(key) as T | undefined
        },
        set<T>(key: string, value: T): T {
          cache.set(key, value)
          return value
        },
        getOrCreate<T>(key: string, compute: () => T): T {
          if (cache.has(key))
            return cache.get(key) as T
          const value = compute()
          cache.set(key, value)
          return value
        },
      }
    },
    clear() {
      rowCaches.clear()
    },
    prune(liveRowVersionKeys) {
      const live = new Set(liveRowVersionKeys)
      for (const key of rowCaches.keys()) {
        if (!live.has(key))
          rowCaches.delete(key)
      }
    },
    size: () => rowCaches.size,
  }
}

export function cachedRenderValue<T>(
  context: { renderCache?: MessageRenderCache } | undefined,
  key: string,
  compute: () => T,
): T {
  return context?.renderCache?.getOrCreate(key, compute) ?? compute()
}

export function cachedRenderValueForString<T>(
  context: { renderCache?: MessageRenderCache } | undefined,
  namespace: string,
  input: string,
  compute: () => T,
): T {
  const cached = getCachedRenderValueForString<T>(context, namespace, input)
  if (cached !== undefined)
    return cached
  return setCachedRenderValueForString(context, namespace, input, compute())
}

interface StringRenderCacheEntry<T> {
  input: string
  value: T
}

export function getCachedRenderValueForString<T>(
  context: { renderCache?: MessageRenderCache } | undefined,
  namespace: string,
  input: string,
): T | undefined {
  const cached = context?.renderCache?.get<StringRenderCacheEntry<T>>(stableStringCacheKey(namespace, input))
  return cached?.input === input ? cached.value : undefined
}

export function setCachedRenderValueForString<T>(
  context: { renderCache?: MessageRenderCache } | undefined,
  namespace: string,
  input: string,
  value: T,
): T {
  context?.renderCache?.set<StringRenderCacheEntry<T>>(stableStringCacheKey(namespace, input), { input, value })
  return value
}

interface StringTupleRenderCacheEntry<T> {
  inputs: readonly string[]
  value: T
}

export function cachedRenderValueForStrings<T>(
  context: { renderCache?: MessageRenderCache } | undefined,
  namespace: string,
  inputs: readonly string[],
  compute: () => T,
): T {
  const key = [
    namespace,
    ...inputs.map(input => stableStringCacheKey('part', input)),
  ].join(':')
  const cached = context?.renderCache?.get<StringTupleRenderCacheEntry<T>>(key)
  if (cached?.inputs.length === inputs.length && cached.inputs.every((input, index) => input === inputs[index]))
    return cached.value
  const value = compute()
  context?.renderCache?.set<StringTupleRenderCacheEntry<T>>(key, { inputs: [...inputs], value })
  return value
}

export function stableStringCacheKey(namespace: string, input: string): string {
  return `${namespace}:${input.length}:${fnv1a32Hex(input)}`
}
