export interface MessageRenderCache {
  get: <T>(key: string) => T | undefined
  set: <T>(key: string, value: T) => T
  getOrCreate: <T>(key: string, compute: () => T) => T
}

export interface MessageRenderCacheStore {
  forRow: (rowVersionKey: string) => MessageRenderCache
  prune: (liveRowVersionKeys: Iterable<string>) => void
  size: () => number
}

const DEFAULT_MAX_RENDER_CACHE_ROWS = 512

export function createMessageRenderCacheStore(maxRows = DEFAULT_MAX_RENDER_CACHE_ROWS): MessageRenderCacheStore {
  const rowCaches = new Map<string, Map<string, unknown>>()

  const touch = (rowVersionKey: string): Map<string, unknown> => {
    let cache = rowCaches.get(rowVersionKey)
    if (cache) {
      rowCaches.delete(rowVersionKey)
      rowCaches.set(rowVersionKey, cache)
      return cache
    }
    cache = new Map<string, unknown>()
    rowCaches.set(rowVersionKey, cache)
    while (rowCaches.size > maxRows) {
      const oldest = rowCaches.keys().next().value
      if (oldest === undefined)
        break
      rowCaches.delete(oldest)
    }
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
  return `${namespace}:${input.length}:${fnv1a32(input)}`
}

function fnv1a32(input: string): string {
  let hash = 0x811C9DC5
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193)
  }
  return (hash >>> 0).toString(36)
}
