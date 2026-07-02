import type { TokenizeRequest, TokenizeResponse } from './shikiWorker'
import type { CachedToken, InternedTokenLines } from './tokenCache'
import { getArtifact, isArtifactStoreAvailable, putArtifact, RENDER_ARTIFACT_CACHE_VERSION } from './renderArtifactStore'
import { DUAL_THEME_TOKEN_OPTIONS } from './shikiThemes'
import { expandInternedTokenLines, getCachedTokens, makeKey, setCachedTokens } from './tokenCache'
import { createWorkerClient } from './workerClient'
import { createWorkerPriorityGate } from './workerPriorityGate'

// The lazy worker lifecycle (spawn / dispatch-by-id / crash recovery) lives in the
// shared factory; this client layers the token cache + in-flight coalescing on top.
// The wire carries the INTERNED token shape (styles table + indices — see
// internTokenLines). It stays interned through the client so persistence can
// store it VERBATIM — the expanded CachedToken form carries only minted class
// names, not the style declarations a later session needs to re-mint them —
// and tokenizeAsync expands it once for the cache + consumers.
const client = createWorkerClient<TokenizeRequest, InternedTokenLines | null>({
  spawn: () => new Worker(new URL('./shikiWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: TokenizeResponse) => ({
    id: data.id,
    value: data.tokens,
  }),
  failureValue: null,
})

// Concurrent identical requests share one in-flight promise so the SAME (lang, code)
// isn't tokenized twice before the first reply caches it -- a virtualized chat row
// re-mounts ~4-5x as it scrolls in/out (and the two diff sides may carry identical
// text), so without this each re-mount dispatches a duplicate worker tokenization on
// a cache miss. Mirrors renderMarkdown's `inFlight` dedup. Keyed identically to the
// token cache (`${lang}\0${code}`); each entry is dropped when its promise settles.
const inFlightByKey = new Map<string, Promise<CachedToken[][] | null>>()

// Dispatch order gate shared by all tokenize requests: viewport code surfaces
// preempt overscan ones (see createWorkerPriorityGate).
const gate = createWorkerPriorityGate()

// --- persisted token artifacts (IndexedDB) ----------------------------------
// Reload warm-start for code surfaces, mirroring renderMarkdown's persisted
// HTML. Values are stored in the compact INTERNED wire shape and expanded on
// read. The namespace folds in the cache version + theme names: persisted
// tokens outlive the bundle, so a wire-shape or theme-contract change must
// orphan old entries (see RENDER_ARTIFACT_CACHE_VERSION).
export const TOKEN_ARTIFACT_NS = `tok@${RENDER_ARTIFACT_CACHE_VERSION}|${DUAL_THEME_TOKEN_OPTIONS.themes.light},${DUAL_THEME_TOKEN_OPTIONS.themes.dark}`

/** One pathological body must not dominate the store (key embeds the code). */
const PERSIST_MAX_KEY_LENGTH = 256 * 1024

/**
 * Look up persisted tokens. Returns undefined SYNCHRONOUSLY when the store
 * can't serve here (no indexedDB, oversized code), so the caller dispatches the
 * worker with its usual same-frame timing.
 */
function getPersistedTokens(key: string): Promise<CachedToken[][] | undefined> | undefined {
  if (!isArtifactStoreAvailable() || key.length > PERSIST_MAX_KEY_LENGTH)
    return undefined
  return getArtifact<InternedTokenLines>(TOKEN_ARTIFACT_NS, key).then((stored) => {
    if (!stored || !Array.isArray(stored.styles) || !Array.isArray(stored.lines))
      return undefined
    return expandInternedTokenLines(stored)
  })
}

function persistTokens(key: string, interned: InternedTokenLines): void {
  if (!isArtifactStoreAvailable() || key.length > PERSIST_MAX_KEY_LENGTH)
    return
  void putArtifact(TOKEN_ARTIFACT_NS, key, interned)
}

/**
 * Tokenize code asynchronously via the Web Worker.
 * Checks the cache first and populates it on completion.
 *
 * `isLowPriority` (re-read at each dispatch opportunity) deprioritizes the
 * request behind currently-high ones. A coalesced duplicate keeps the FIRST
 * caller's priority — acceptable staleness: identical (lang, code) in two
 * rows at different priorities is rare, and the result caches for both.
 */
export function tokenizeAsync(
  lang: string,
  code: string,
  isLowPriority?: () => boolean,
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

  const dispatchToWorker = (): Promise<CachedToken[][] | null> => gate
    .enqueue(() => client.request(id => ({ type: 'tokenize', id, lang, code })), isLowPriority)
    .then((interned) => {
      if (!interned)
        return null
      // Cache before the value propagates to consumers (a `.then` runs before the awaiter's
      // continuation), so a caller that reads the cache after awaiting sees it populated.
      const tokens = expandInternedTokenLines(interned)
      setCachedTokens(lang, code, tokens)
      persistTokens(key, interned)
      return tokens
    })

  // Reload warm-start: serve persisted tokens when they exist, else dispatch.
  // Without a store `persisted` is a SYNCHRONOUS undefined, preserving the
  // dispatch's same-frame timing. The in-flight entry covers the WHOLE chain
  // (store lookup + worker), so concurrent callers coalesce on either path.
  const persisted = getPersistedTokens(key)
  const promise = (
    persisted === undefined
      ? dispatchToWorker()
      : persisted.then((stored) => {
          if (stored) {
            setCachedTokens(lang, code, stored)
            return stored
          }
          return dispatchToWorker()
        })
  )
    .finally(() => {
      // Drop the in-flight entry once settled (resolved by the worker reply or by the
      // factory's failure path) so a later request re-dispatches.
      inFlightByKey.delete(key)
    })
  inFlightByKey.set(key, promise)
  return promise
}
