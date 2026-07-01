import type { TokenizeRequest, TokenizeResponse } from './shikiWorker'
import type { CachedToken } from './tokenCache'
import { getCachedTokens, makeKey, setCachedTokens } from './tokenCache'
import { createWorkerClient } from './workerClient'

// The lazy worker lifecycle (spawn / dispatch-by-id / crash recovery) lives in the
// shared factory; this client layers the token cache + in-flight coalescing on top.
const client = createWorkerClient<TokenizeRequest, CachedToken[][] | null>({
  spawn: () => new Worker(new URL('./shikiWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: TokenizeResponse) => ({ id: data.id, value: data.tokens }),
  failureValue: null,
})

// Concurrent identical requests share one in-flight promise so the SAME (lang, code)
// isn't tokenized twice before the first reply caches it -- a virtualized chat row
// re-mounts ~4-5x as it scrolls in/out (and the two diff sides may carry identical
// text), so without this each re-mount dispatches a duplicate worker tokenization on
// a cache miss. Mirrors renderMarkdown's `inFlight` dedup. Keyed identically to the
// token cache (`${lang}\0${code}`); each entry is dropped when its promise settles.
const inFlightByKey = new Map<string, Promise<CachedToken[][] | null>>()

/**
 * Tokenize code asynchronously via the Web Worker.
 * Checks the cache first and populates it on completion.
 */
export function tokenizeAsync(
  lang: string,
  code: string,
): Promise<CachedToken[][] | null> {
  const cached = getCachedTokens(lang, code)
  if (cached)
    return Promise.resolve(cached)

  if (typeof Worker === 'undefined')
    return Promise.resolve(null)

  // Coalesce a concurrent identical request onto the existing in-flight promise.
  const key = makeKey(lang, code)
  const inFlight = inFlightByKey.get(key)
  if (inFlight)
    return inFlight

  const promise = client
    .request(id => ({ type: 'tokenize', id, lang, code }))
    .then((tokens) => {
      // Cache before the value propagates to consumers (a `.then` runs before the awaiter's
      // continuation), so a caller that reads the cache after awaiting sees it populated.
      if (tokens)
        setCachedTokens(lang, code, tokens)
      return tokens
    })
    .finally(() => {
      // Drop the in-flight entry once settled (resolved by the worker reply or by the
      // factory's failure path) so a later request re-dispatches.
      inFlightByKey.delete(key)
    })
  inFlightByKey.set(key, promise)
  return promise
}
