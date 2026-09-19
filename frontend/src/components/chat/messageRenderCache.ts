import { lruGet, lruSet } from '~/lib/mapLru'
import { fnv1a32Hex } from '~/lib/stringDigest'

// ---------------------------------------------------------------------------
// The per-row render cache, keyed by VALUES the store cannot re-derive.
//
// A key is OPAQUE and TYPED: no caller spells a string and picks the value's type
// at the read, which is the two-way guess the old `get<T>(key: string)` allowed --
// a mistyped read answered whatever object sat at that string, and the wrong type
// then travelled as far from the cache as the renderer that consumed it. Here the
// key's own type states the entry it addresses, and the factories are the only way
// to build one.
// ---------------------------------------------------------------------------

/**
 * One cache entry's address: opaque, and typed by the value it holds.
 *
 * The phantom member carries `T` and nothing at runtime. Two keys built by two
 * factories answer two different tokens, and a key of one entry type is not
 * assignable where another's is expected -- so a `set` and the `get` that reads it
 * back cannot disagree about what the entry is.
 */
declare const cacheKeyBrand: unique symbol
export interface CacheKey<T> {
  readonly token: string
  readonly [cacheKeyBrand]: (value: T) => void
}

/** Build a key for an entry type only this module's factories name. The one assertion branding needs. */
function brandedKey<T>(token: string): CacheKey<T> {
  return { token } as CacheKey<T>
}

/** The entry one hashed string input addresses: the input itself, kept to verify the hash at read. */
export interface StringRenderCacheEntry<T> {
  input: string
  value: T
}

/** The entry a tuple of hashed string inputs addresses: the inputs, kept to verify each hash at read. */
export interface StringTupleRenderCacheEntry<T> {
  inputs: readonly string[]
  value: T
}

/**
 * The key for one value under a FIXED name -- the row IR, per row-revision cache.
 *
 * The row cache is per-revision by construction (`forRow` hands each revision its
 * own map), so its key needs no input folded in.
 */
export function fixedCacheKey<T>(name: string): CacheKey<T> {
  return brandedKey<T>(name)
}

/**
 * The key for one hashed string input, such as a markdown body or an ANSI block.
 *
 * The token carries the input's LENGTH and digest, never the input itself: a
 * streaming row's bodies would otherwise sit in the key twice. The digest can
 * collide, so the entry keeps the input and the read compares it -- see
 * {@link getCachedRenderValueForString}.
 */
export function stringCacheKey<T>(namespace: string, input: string): CacheKey<StringRenderCacheEntry<T>> {
  return brandedKey(`${namespace}:${input.length}:${fnv1a32Hex(input)}`)
}

/**
 * The key for a tuple of hashed string inputs, such as a diff's two bodies and the
 * file they belong to. Collision verification per part, as above.
 */
export function stringTupleCacheKey<T>(namespace: string, inputs: readonly string[]): CacheKey<StringTupleRenderCacheEntry<T>> {
  return brandedKey([
    namespace,
    ...inputs.map(input => `${input.length}:${fnv1a32Hex(input)}`),
  ].join(':'))
}

export interface MessageRenderCache {
  get: <T>(key: CacheKey<T>) => T | undefined
  set: <T>(key: CacheKey<T>, value: T) => T
  getOrCreate: <T>(key: CacheKey<T>, compute: () => T) => T
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
        // The one unchecked lookup in the module: `set` writes an entry under a key
        // of the same type, so the map's `unknown` holds what the key states.
        get<T>(key: CacheKey<T>): T | undefined {
          return cache.get(key.token) as T | undefined
        },
        set<T>(key: CacheKey<T>, value: T): T {
          cache.set(key.token, value)
          return value
        },
        getOrCreate<T>(key: CacheKey<T>, compute: () => T): T {
          if (cache.has(key.token))
            return cache.get(key.token) as T
          const value = compute()
          cache.set(key.token, value)
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

// The context params below allow an explicit `undefined` renderCache: a full
// MarkdownRenderContext/RenderContext flows in here, and its reactive getter
// answers undefined while the owning row is absent for now. Every reader
// optional-chains, so undefined stays the live "no cache yet" state.
export function cachedRenderValueForString<T>(
  context: { renderCache?: MessageRenderCache | undefined } | undefined,
  namespace: string,
  input: string,
  compute: () => T,
): T {
  const cached = getCachedRenderValueForString<T>(context, namespace, input)
  if (cached !== undefined)
    return cached
  return setCachedRenderValueForString(context, namespace, input, compute())
}

export function getCachedRenderValueForString<T>(
  context: { renderCache?: MessageRenderCache | undefined } | undefined,
  namespace: string,
  input: string,
): T | undefined {
  // Collision verification: the digest can agree for two inputs, so the entry
  // carries the input it was built from and the read compares it before answering.
  const cached = context?.renderCache?.get(stringCacheKey<T>(namespace, input))
  return cached?.input === input ? cached.value : undefined
}

export function setCachedRenderValueForString<T>(
  context: { renderCache?: MessageRenderCache | undefined } | undefined,
  namespace: string,
  input: string,
  value: T,
): T {
  context?.renderCache?.set(stringCacheKey<T>(namespace, input), { input, value })
  return value
}

export function cachedRenderValueForStrings<T>(
  context: { renderCache?: MessageRenderCache | undefined } | undefined,
  namespace: string,
  inputs: readonly string[],
  compute: () => T,
): T {
  // Collision verification per part, for the reason the single-input read gives.
  const cached = context?.renderCache?.get(stringTupleCacheKey<T>(namespace, inputs))
  if (cached?.inputs.length === inputs.length && cached.inputs.every((input, index) => input === inputs[index]))
    return cached.value
  const value = compute()
  context?.renderCache?.set(stringTupleCacheKey<T>(namespace, inputs), { inputs: [...inputs], value })
  return value
}
