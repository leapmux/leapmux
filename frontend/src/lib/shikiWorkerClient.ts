import type { TokenizeRequest, TokenizeResponse } from './shikiWorker'
import type { CachedToken, InternedTokenLines } from './tokenCache'
import { createPersistedArtifact } from './persistedArtifact'
import { syntaxThemeKey, syntaxThemePair, themeStillCurrent } from './shikiThemes'
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
// Reload warm-start for code surfaces. Values are stored in the compact
// INTERNED wire shape and expanded on read.

/** One pathological body must not dominate the store (key embeds the code). */
const PERSIST_MAX_KEY_LENGTH = 256 * 1024

function isStringRecord(value: unknown): value is Record<string, string> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    && Object.values(value).every(v => typeof v === 'string')
}

function isPersistedStyle(value: unknown): value is string | Record<string, string> {
  return typeof value === 'string' || isStringRecord(value)
}

/** The stored wire shape, narrowed before anything expands it. */
function isInternedTokenLines(value: unknown): value is InternedTokenLines {
  if (value === null || typeof value !== 'object' || Array.isArray(value))
    return false
  const { styles, lines } = value as { styles?: unknown, lines?: unknown }
  if (!Array.isArray(styles) || !styles.every(isPersistedStyle) || !Array.isArray(lines))
    return false
  return lines.every(line => Array.isArray(line) && line.every((token) => {
    if (!Array.isArray(token) || token.length !== 2)
      return false
    const [styleIndex, content] = token
    return Number.isInteger(styleIndex)
      && styleIndex >= -1
      && styleIndex < styles.length
      && typeof content === 'string'
  }))
}

const tokenArtifact = createPersistedArtifact<InternedTokenLines, CachedToken[][]>({
  prefix: 'tok',
  maxSourceLength: PERSIST_MAX_KEY_LENGTH,
  isValid: isInternedTokenLines,
  decode: (stored) => {
    try {
      return expandInternedTokenLines(stored)
    }
    catch {
      return undefined
    }
  },
})

export const tokenArtifactNs = tokenArtifact.ns

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

  // The pair this request is answered under, captured ONCE at dispatch and
  // carried through the wire message, the coalescing key and the artifact
  // namespace, so all four name the same theme however long the round trip takes.
  const pair = syntaxThemePair()
  const ns = tokenArtifactNs()

  // Coalesce a concurrent identical request onto the existing in-flight promise.
  //
  // The theme is part of the key. Without it a request issued AFTER a theme
  // change coalesced onto one dispatched BEFORE it and received the abandoned
  // theme's tokens, which then went into the shared token cache -- a cache the
  // theme-change invalidator had already emptied, and whose own key carries no
  // theme, so every later reader served the wrong colours until the entry was
  // evicted.
  const cacheKey = makeKey(lang, code)
  const flightKey = `${syntaxThemeKey()}\u0000${cacheKey}`
  const inFlight = inFlightByKey.get(flightKey)
  if (inFlight)
    return inFlight

  const dispatchToWorker = (): Promise<CachedToken[][] | null> => gate
    .enqueue(() => client.request(id => ({ type: 'tokenize', id, lang, code, syntax: pair })), isLowPriority)
    .then((interned) => {
      if (!interned)
        return null
      // Cache before the value propagates to consumers (a `.then` runs before the awaiter's
      // continuation), so a caller that reads the cache after awaiting sees it populated.
      const tokens = expandInternedTokenLines(interned)
      // Only when the theme has not moved under us. The in-memory cache keys on
      // (lang, code) alone, so writing a reply that belongs to an abandoned
      // theme would repopulate the cache the invalidator just cleared.
      if (themeStillCurrent(pair))
        setCachedTokens(lang, code, tokens)
      tokenArtifact.write(ns, cacheKey, interned)
      return tokens
    })

  // Reload warm-start: serve persisted tokens when they exist, else dispatch.
  // Without a store `persisted` is a SYNCHRONOUS undefined, preserving the
  // dispatch's same-frame timing. The in-flight entry covers the WHOLE chain
  // (store lookup + worker), so concurrent callers coalesce on either path.
  const persisted = tokenArtifact.read(cacheKey)
  const promise = (
    persisted === undefined
      ? dispatchToWorker()
      : persisted.then((stored) => {
          if (stored) {
            // No theme test here: `read` answers a MISS when the pair moved
            // during its round trip, so `stored` belongs to the pair showing
            // now. The rule lives in `~/lib/persistedArtifact` rather than at
            // each caller, because this caller is the one that forgot it -- a
            // stale hit repopulated the cache the invalidator had just emptied,
            // under a key that carries no theme.
            setCachedTokens(lang, code, stored)
            return stored
          }
          return dispatchToWorker()
        })
  )
    .finally(() => {
      // Drop the in-flight entry once settled (resolved by the worker reply or by the
      // factory's failure path) so a later request re-dispatches.
      inFlightByKey.delete(flightKey)
    })
  inFlightByKey.set(flightKey, promise)
  return promise
}
