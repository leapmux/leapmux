/** Serializable token: only the fields ReadResultView actually uses. */
export interface CachedToken {
  content: string
  htmlStyle?: string | Record<string, string>
}

const CACHE_MAX_SIZE = 64

const cache = new Map<string, CachedToken[][]>()

function makeKey(lang: string, code: string): string {
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
  if (cache.size >= CACHE_MAX_SIZE) {
    const firstKey = cache.keys().next().value!
    cache.delete(firstKey)
  }
  cache.set(key, tokens)
}
