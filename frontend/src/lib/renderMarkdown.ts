import type { MarkdownRenderResult } from './markdownWorkerClient'
import type { WorkerDemand } from './workerDemand'
import { capMapInsertionOrder, lruGet, lruSet } from '~/lib/mapLru'
import { markdownSourceFallbackHtml } from './markdownSourceFallback'
import { renderMarkdownInWorker } from './markdownWorkerClient'
import { createPersistedArtifact } from './persistedArtifact'
import { ensureShikiStyleRules, isShikiStyleClassName, shikiStylesUsedBy } from './shikiStyleClass'
import { syntaxThemePair, themeStillCurrent } from './shikiThemes'
import { onSyntaxThemeChange } from './syntaxThemeStore'
import { createCoalescedVersion, createWorkerDemand } from './workerDemand'

// LRU cache for rendered markdown HTML: avoids re-rendering identical content on
// re-mount (the virtualized chat list mounts a row ~4-5x as it scrolls in and out).
// Holds the FINAL result of the worker, so a cache hit serves the final HTML with no
// flash. Sized well above a viewport's worth of distinct messages so a normal scroll
// session stops re-rendering rather than re-paying the worker round-trip after
// eviction.
const CACHE_MAX_SIZE = 1024

/**
 * The most characters (UTF-16 code units, as `String.length` counts them) that
 * the cached entries hold together, counting the source text (the key) and the
 * HTML (the value) of each entry. Write every entry through cacheMarkdownHtml,
 * which keeps this limit.
 *
 * The entry count alone does not limit memory, because a body of any size
 * reaches this cache. A highlighted render measured about 9 times the length of
 * its source for fenced code and about 2 times for prose, so one 2 MB tool
 * result of code holds about 20 million characters. V8 stores one or two bytes
 * per character, so this limit is 32 to 64 MB. A session of ordinary bodies, a
 * few KB each, stays far below it, and there the entry count decides.
 */
export const MARKDOWN_CACHE_MAX_CHARS = 32 * 1024 * 1024

const markdownCache = new Map<string, string>()

// Bumped whenever an async worker render completes and fills the cache, so a
// consumer that called requestMarkdownHtml in a reactive context (every chat call
// site does, via a memo) re-renders and picks up the HTML it was waiting for.
// Module-level so all markdown consumers share one invalidation; the memo equality
// check means only the bodies whose HTML actually changed touch the DOM. A burst of
// completions (a screenful of bodies finishing within a tick) bumps it once.
const markdownVersion = createCoalescedVersion()

/**
 * The renders in flight, keyed by text. Concurrent and re-rendered consumers of
 * the same body share one render rather than each dispatching a duplicate, and
 * the priority gate reads the render's demand at each dispatch opportunity: a
 * prefetch alone leaves it low, and a consumer raises it while its row needs the
 * body (see createWorkerDemand).
 *
 * An entry belongs to the render that created it, and only that render removes
 * it (see ensureRender). A theme change and a reset clear the map, which
 * abandons every render in flight.
 */
const inFlight = new Map<string, WorkerDemand>()

// Per-text count of transient-failure retries (a grammar chunk load / highlighter init
// that failed and may recover). Limits re-dispatch so a genuinely broken grammar
// eventually caches its degraded render instead of re-dispatching forever.
const retryCount = new Map<string, number>()
const MAX_MARKDOWN_RENDER_RETRIES = 3

// --- persisted highlighted-markdown artifacts (IndexedDB) -------------------
// Reload warm-start: a clean worker render is served on the next session
// without re-highlighting.

// Per-entry bounds: one pathological body must not dominate the store.
const PERSIST_MAX_TEXT_LENGTH = 256 * 1024
const PERSIST_MAX_HTML_LENGTH = 512 * 1024

/**
 * Persisted artifact value: the highlighted HTML plus the className ->
 * declaration dictionary for the shared token-style classes it references
 * (see shikiStyleClass). The HTML is useless without the dictionary — a later
 * session must re-inject the rules before the class names mean anything.
 */
interface PersistedMarkdownArtifact {
  h: string
  s: Record<string, string>
}

function isPersistedMarkdownArtifact(value: unknown): value is PersistedMarkdownArtifact {
  if (value === null || typeof value !== 'object' || Array.isArray(value))
    return false
  const { h, s } = value as { h?: unknown, s?: unknown }
  if (typeof h !== 'string' || h.length > PERSIST_MAX_HTML_LENGTH)
    return false
  if (s === null || typeof s !== 'object' || Array.isArray(s))
    return false
  return Object.entries(s).every(([className, decl]) =>
    isShikiStyleClassName(className) && typeof decl === 'string')
}

const markdownArtifact = createPersistedArtifact<PersistedMarkdownArtifact, MarkdownRenderResult>({
  prefix: 'md',
  maxSourceLength: PERSIST_MAX_TEXT_LENGTH,
  isValid: isPersistedMarkdownArtifact,
  decode: value => ({ html: value.h, retryable: false, styles: value.s }),
})

export const markdownArtifactNs = markdownArtifact.ns

/**
 * Drop every cached render and abandon every render in flight. Each map keyed by
 * text belongs here, so that no path can clear one and leave another stale.
 */
function clearRenderState(): void {
  markdownCache.clear()
  inFlight.clear()
  retryCount.clear()
}

// A rendered markdown body carries Shiki's baked token colours, so it is stale
// the moment the syntax theme changes. The IndexedDB copies are orphaned instead
// of dropped -- `markdownArtifactNs()` folds the theme in, so entries written
// under the old pair are simply never looked up again, and the store's TTL sweep
// collects them.

onSyntaxThemeChange(() => {
  // `inFlight` and `retryCount` go with the cache, exactly as in
  // `_resetMarkdownCache`. A body still rendering under the OLD pair would
  // otherwise hit the dedup guard in `ensureRender` (`inFlight.get(text)`), so
  // the re-render after a theme change never re-dispatched -- and when the old
  // reply landed it wrote the abandoned theme's HTML into the cache this line
  // just cleared and bumped the version, so the row repainted in the theme the
  // user had just left while its neighbours moved on.
  clearRenderState()
  // Then TELL the consumers, the way every other mutation of these caches does.
  // A chat row happens to re-render anyway, because its own cache namespace
  // folds in `syntaxThemeGeneration()`. A consumer that reads only
  // `markdownVersion` -- a Markdown body with no render context, such as the
  // file viewer's, whose memo depends on the text and nothing else -- was never
  // told, so an open markdown file kept the abandoned theme's baked token
  // colours against a code surface repainted for the new one, until some
  // unrelated body's completion bumped the version.
  markdownVersion.bump()
})

/** Visible for testing: drop all cached entries and in-flight tracking. */
export function _resetMarkdownCache(): void {
  // Clear inFlight too: a text left "in flight" (its worker render never resolved)
  // would otherwise be skipped forever by the dedup guard, so a clear-and-retry could
  // never actually retry -- and a text dispatched in one test would leak into the next.
  clearRenderState()
}

/** Visible for testing: number of cached entries. */
export function _getMarkdownCacheSize(): number {
  return markdownCache.size
}

/** The characters that the cached entries hold together (see MARKDOWN_CACHE_MAX_CHARS). */
function markdownCacheChars(): number {
  let chars = 0
  for (const [text, html] of markdownCache)
    chars += text.length + html.length
  return chars
}

/** Visible for testing: the characters that the cached entries hold together. */
export function _getMarkdownCacheChars(): number {
  return markdownCacheChars()
}

/**
 * Cache `html` as the final render of `text`, at the most-recently-used end. Then
 * evict the least-recently-used entries until the cache keeps both of its limits.
 *
 * It never evicts the entry that it just wrote, even when that entry alone holds
 * more than MARKDOWN_CACHE_MAX_CHARS. A consumer waits for it and reads it after
 * the version bump. A miss there would dispatch the body again, and again after
 * each reply.
 */
function cacheMarkdownHtml(text: string, html: string): void {
  lruSet(markdownCache, text, html, CACHE_MAX_SIZE)
  let chars = markdownCacheChars()
  for (const [cachedText, cachedHtml] of markdownCache) {
    if (chars <= MARKDOWN_CACHE_MAX_CHARS || cachedText === text)
      break
    markdownCache.delete(cachedText)
    chars -= cachedText.length + cachedHtml.length
  }
}

/**
 * Return completed markdown HTML from the shared cache without subscribing to
 * worker-completion invalidations or dispatching a render. Selection-preserving
 * chat renders use this to keep already-displayed DOM stable.
 */
export function getCachedMarkdownHtml(text: string): string | undefined {
  return lruGet(markdownCache, text) // hit re-fronts to MRU end
}

/**
 * Start the off-thread render of `text` unless one is in flight, and return its
 * in-flight entry. The render always completes later, never in this call.
 *
 * The caller has checked the cache.
 */
function ensureRender(text: string): WorkerDemand {
  const existing = inFlight.get(text)
  if (existing !== undefined)
    return existing
  const render = createWorkerDemand()
  inFlight.set(text, render)
  // The theme this dispatch belongs to, captured once. The reply may land
  // after the user changed it, and a body highlighted under the abandoned
  // theme must neither be cached nor persisted under the new one -- the
  // invalidator already cleared the cache, so writing into it would restore
  // exactly what the clear removed and no later read would re-dispatch,
  // because a cache hit never does.
  const dispatchPair = syntaxThemePair()
  const dispatchNs = markdownArtifactNs()
  const complete = (result: MarkdownRenderResult | null): void => {
    // Inject the reply's shared token-style rules FIRST, whatever becomes of its
    // HTML: the worker minted the class names but has no document (see
    // shikiStyleClass). The worker sends each declaration only once, so a rule
    // dropped here is never sent again, and a later render that uses its class
    // shows uncoloured tokens. Injection is idempotent per class. It is also the
    // one step here that can throw, so it runs before the bookkeeping below: a
    // throw leaves the render in flight, for `complete(null)` to finish.
    if (result !== null)
      ensureShikiStyleRules(result.styles)
    // Only the render that owns the entry may finish the body. A theme change or
    // a reset abandons the render (see `inFlight`), and a later request may then
    // have started a newer render of the same body. An abandoned render therefore
    // leaves the entry, the retry count and the cache alone.
    if (inFlight.get(text) !== render)
      return
    inFlight.delete(text)
    // A render that still owned its entry ran under the current theme, because
    // every theme change abandons the renders in flight. The check stays because
    // tokenized output must pass it before every write (see themeStillCurrent).
    // The entry is released already, so a later request renders the body again.
    if (!themeStillCurrent(dispatchPair))
      return
    // A transient degrade (a grammar chunk load or the highlighter init failed): the
    // render is (partly) plain but a retry may recover it, so DON'T cache it. Bump the
    // version so a re-render re-dispatches -- limited, so a grammar that never loads
    // still caches its degraded render eventually instead of re-dispatching forever.
    if (result?.retryable && (retryCount.get(text) ?? 0) < MAX_MARKDOWN_RENDER_RETRIES) {
      // delete+set moves this actively-retrying text to the most-recently-used end BEFORE
      // capping. A bare set() on an existing key keeps its original insertion position, so
      // capMapInsertionOrder could evict the very entry still bouncing through the retry
      // loop -- resetting its count and re-granting the full budget. Moving it to MRU means
      // the cap evicts a genuinely idle entry instead, keeping the retry bound meaningful.
      const next = (retryCount.get(text) ?? 0) + 1
      retryCount.delete(text)
      retryCount.set(text, next)
      // Cap the map: entries are otherwise removed only on the final path below, so a
      // text that degrades retryably and whose reactive consumer then unmounts (scrolled
      // away before the version bump re-dispatches) would leak forever -- unlike the
      // LRU cache. Evicting the insertion-order-oldest entry at worst resets a long-idle
      // text's retry count, which just grants it the full budget again on a later re-render.
      capMapInsertionOrder(retryCount, CACHE_MAX_SIZE)
      markdownVersion.bump()
      return
    }
    retryCount.delete(text)
    // A null result means no worker could render: it crashed, could not start (also
    // where no Worker exists), rejected, or ran past the render's time budget. Show
    // the escaped source rather than parse on the main thread, and cache it, so the
    // body does not re-dispatch to a broken worker, or tie up a fresh one again,
    // forever.
    cacheMarkdownHtml(text, result?.html ?? markdownSourceFallbackHtml(text))
    markdownVersion.bump()
  }
  // Persist only a CLEAN highlighted render for the reload warm-start: a retryable
  // degrade or a crash (null) is not a durable result. The HTML length is checked
  // HERE rather than inside the shared helper: it limits the VALUE, and only this
  // consumer has one whose size is worth limiting apart from its key.
  //
  // Store the declaration of every token-style class that the HTML uses, not the
  // reply's own: a reply carries only the declarations that its worker minted for
  // the first time. A render whose dictionary this thread cannot complete is not
  // persisted, because a reload could not colour its tokens.
  const persist = (result: MarkdownRenderResult | null): void => {
    if (result === null || result.retryable || result.html.length > PERSIST_MAX_HTML_LENGTH)
      return
    const styles = shikiStylesUsedBy(result.html)
    if (styles !== undefined)
      markdownArtifact.write(dispatchNs, text, { h: result.html, s: styles })
  }
  // The `Promise` constructor turns a synchronous throw of the worker bridge into a
  // rejection, so every failure takes one path: the `catch`, which shows the source.
  // `persist` runs after `complete`, which injected the reply's own rules: the
  // dictionary that it stores needs them.
  const dispatchToWorker = (): void => {
    void new Promise<MarkdownRenderResult | null>(resolve => resolve(renderMarkdownInWorker(text, dispatchPair, render.isLow)))
      .then((result) => {
        complete(result)
        persist(result)
      })
      .catch(() => complete(null))
  }
  // Reload warm-start: serve the persisted highlighted render when one exists,
  // else dispatch. Without a store this is a SYNCHRONOUS undefined, so the
  // dispatch keeps its same-frame timing.
  const persisted = markdownArtifact.read(text)
  if (persisted === undefined) {
    dispatchToWorker()
    return render
  }
  // Both handlers are load-bearing: a consumer waits for this render, and a chat row
  // stays hidden until it lands. A failed READ falls back to the worker. A failed
  // completion ends as a worker's does, with the escaped source; the render still
  // owns its entry then (see `complete`), so no other render of the body starts.
  void persisted
    .then(
      (result) => {
        if (result === undefined)
          dispatchToWorker()
        else
          complete(result)
      },
      () => dispatchToWorker(),
    )
    .catch(() => complete(null))
  return render
}

/**
 * Return the final HTML of a Markdown body, or undefined while it renders.
 *
 * This NEVER parses Markdown on the main thread: a single synchronous parse
 * measured about 1.5 s for a 512 KB body in V8, and remark recurses deeply enough
 * to overflow the stack on nested input. A cache miss dispatches the render to a
 * worker and returns undefined; when the worker answers, the result is cached and
 * a version signal bumps, so a reactive caller reads again and receives the HTML.
 * The caller shows its pending state until then, and a chat row stays hidden
 * behind its loading skeleton.
 *
 * `isLowPriority` (re-read at each dispatch opportunity) orders this body's
 * worker render behind currently-high ones -- rows outside the near-viewport
 * band pass it so viewport rows render first (see createWorkerPriorityGate). A
 * caller that passes none raises the body to full priority.
 */
export function requestMarkdownHtml(text: string, isLowPriority?: () => boolean): string | undefined {
  // An empty body renders to nothing, so it needs no worker round trip.
  if (text === '')
    return ''

  // Subscribe to the version signal so an async worker completion re-renders this
  // (reactive) caller. Read BEFORE the cache lookup so the dependency is always
  // registered, including on the cache-hit path.
  markdownVersion.read()

  const cached = lruGet(markdownCache, text) // hit re-fronts to MRU end
  if (cached !== undefined)
    return cached

  ensureRender(text).note(isLowPriority)
  return undefined
}

/**
 * Start rendering `text` at low priority, so a body that mounts later -- the body
 * of a collapsed section, when the reader expands it -- finds its HTML cached.
 * Subscribes to nothing and returns nothing. A consumer that requests the same
 * body before it lands raises its priority (see createWorkerDemand).
 */
export function prefetchMarkdownHtml(text: string): void {
  if (text === '' || markdownCache.has(text))
    return
  ensureRender(text)
}
