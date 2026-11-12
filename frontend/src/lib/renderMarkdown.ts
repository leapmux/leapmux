import type { MarkdownRenderResult } from './markdownWorkerClient'
import { createSignal } from 'solid-js'
import { capMapInsertionOrder, lruGet, lruSet } from '~/lib/mapLru'
import { createMarkdownProcessor, renderWithPlainFallback } from './markdownProcessor'
import { markdownSourceFallbackHtml } from './markdownSourceFallback'
import { renderMarkdownInWorker } from './markdownWorkerClient'
import { createPersistedArtifact } from './persistedArtifact'
import { ensureShikiStyleRules } from './shikiStyleClass'
import { shikiHighlighter } from './shikiSyncHighlighter'
import { createPairKeyedCache, syntaxThemePair, themeStillCurrent } from './shikiThemes'
import { onSyntaxThemeChange } from './syntaxThemeStore'

// The full markdown processor on the MAIN thread. Used only where no Worker exists,
// which means the jsdom unit tests -- in the browser the worker renders instead, off
// the UI thread (see requestMarkdownHtml). It highlights with the synchronous
// highlighter, which holds no grammar, so a fenced block shows its text without token
// colours unless a test loads the grammar (see shikiSyncHighlighter).
//
// Built LAZILY and keyed on the pair, for the same reason the worker's copy is:
// `createMarkdownProcessor` bakes the pair in, so a module-scope `const` froze
// this path on the pair that was current at IMPORT time -- always the static
// Default pair, since nothing can have chosen a theme before this module
// evaluates. Every fallback render then highlighted in GitHub's colours whatever
// the user picked, and cached that under the chosen theme's key.
const syncProcessorFor = createPairKeyedCache<ReturnType<typeof createMarkdownProcessor>>()

function getSyncProcessor(): ReturnType<typeof createMarkdownProcessor> {
  const pair = syntaxThemePair()
  return syncProcessorFor(pair, () => createMarkdownProcessor(shikiHighlighter, pair))
}

// LRU cache for rendered markdown HTML: avoids re-rendering identical content on
// re-mount (the virtualized chat list mounts a row ~4-5x as it scrolls in and out).
// Holds the FINAL result -- whether produced synchronously (no Worker) or by the
// worker -- so a cache hit serves the final HTML with no flash. Sized well above a
// viewport's worth of distinct messages so a normal scroll session stops re-rendering
// rather than re-paying the worker round-trip after eviction.
const CACHE_MAX_SIZE = 1024
const markdownCache = new Map<string, string>()

// Bumped whenever an async worker render completes and fills the cache, so a
// consumer that called requestMarkdownHtml in a reactive context (every chat call
// site does, via a memo) re-renders and picks up the HTML it was waiting for.
// Module-level so all markdown consumers share one invalidation; the memo equality
// check means only the bodies whose HTML actually changed touch the DOM.
const [markdownVersion, setMarkdownVersion] = createSignal(0)

/**
 * One body whose render is in flight. Concurrent and re-rendered consumers of the
 * same body share it rather than each dispatching a duplicate render, and the
 * priority gate reads its demand at each dispatch opportunity.
 */
interface InFlightRender {
  /** True once a consumer with no priority thunk asked for this body. Never resets. */
  urgent: boolean
  /** The latest priority thunk that a consumer supplied (its row's offscreen test). */
  isLowPriority: (() => boolean) | undefined
}

const inFlight = new Map<string, InFlightRender>()

/**
 * Whether the gate may hold `render` behind other jobs. A prefetch alone leaves the
 * render low: nobody waits for it yet. A consumer raises it, so a body that a
 * prefetch queued low does not keep a row that now needs it waiting behind the
 * whole low-priority queue.
 */
function isLowPriorityRender(render: InFlightRender): boolean {
  if (render.urgent)
    return false
  return render.isLowPriority?.() ?? true
}

/** Record that a consumer waits for `render`, at the priority its thunk states. */
function noteConsumerDemand(render: InFlightRender, isLowPriority: (() => boolean) | undefined): void {
  if (isLowPriority === undefined)
    render.urgent = true
  else
    render.isLowPriority = isLowPriority
}

// Per-text count of transient-failure retries (a grammar chunk load / highlighter init
// that failed and may recover). Bounds re-dispatch so a genuinely broken grammar
// eventually caches its degraded render instead of re-dispatching forever -- mirrors
// the editor parser's MAX_LANG_LOAD_RETRIES budget, keeping the three Oniguruma
// consumers' recovery policy consistent.
const retryCount = new Map<string, number>()
const MAX_MARKDOWN_RENDER_RETRIES = 3
// Coalesce a burst of completions (a screenful of bodies finishing within a tick)
// into a single version bump, so consumers re-render once rather than once per body.
let bumpScheduled = false
function scheduleVersionBump(): void {
  if (bumpScheduled)
    return
  bumpScheduled = true
  queueMicrotask(() => {
    bumpScheduled = false
    setMarkdownVersion(v => v + 1)
  })
}

// Whether off-thread rendering is available. False under the jsdom unit tests, which
// define no Worker (the app renders nothing on a server). There requestMarkdownHtml
// renders synchronously, through the same pipeline as the worker, so the output has
// the browser's structure; only fenced code lacks token colours (see getSyncProcessor).
function canUseWorker(): boolean {
  return typeof Worker !== 'undefined'
}

// --- persisted highlighted-markdown artifacts (IndexedDB) -------------------
// Reload warm-start: a clean worker render is served on the next session
// without re-highlighting.

// Per-entry bounds: one pathological body must not dominate the store.
const PERSIST_MAX_TEXT_LENGTH = 256 * 1024
const PERSIST_MAX_HTML_LENGTH = 512 * 1024
const RE_SHIKI_STYLE_CLASS = /^sk-[0-9a-f]{8}-[0-9a-z]+$/

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
    RE_SHIKI_STYLE_CLASS.test(className) && typeof decl === 'string')
}

const markdownArtifact = createPersistedArtifact<PersistedMarkdownArtifact, MarkdownRenderResult>({
  prefix: 'md',
  maxSourceLength: PERSIST_MAX_TEXT_LENGTH,
  isValid: isPersistedMarkdownArtifact,
  decode: value => ({ html: value.h, retryable: false, styles: value.s }),
})

export const markdownArtifactNs = markdownArtifact.ns

// A rendered markdown body carries Shiki's baked token colours, so it is stale
// the moment the syntax theme changes. The IndexedDB copies are orphaned instead
// of dropped -- `markdownArtifactNs()` folds the theme in, so entries written
// under the old pair are simply never looked up again, and the store's TTL sweep
// collects them.

onSyntaxThemeChange(() => {
  markdownCache.clear()
  // `inFlight` and `retryCount` go with it, exactly as `_resetMarkdownCache`
  // does. A body still rendering under the OLD pair is skipped by the
  // `inFlight.has(text)` dedup guard, so the re-render after a theme change
  // never re-dispatched -- and when the old reply landed it wrote the abandoned
  // theme's HTML into the cache this line just cleared and bumped the version,
  // so the row repainted in the theme the user had just left while its
  // neighbours moved on.
  inFlight.clear()
  retryCount.clear()
  // Then TELL the consumers, the way every other mutation of these caches does.
  // A chat row happens to re-render anyway, because its own cache namespace
  // folds in `syntaxThemeGeneration()`. A consumer that reads only
  // `markdownVersion()` -- a Markdown body with no render context, such as the
  // file viewer's, whose memo depends on the text and nothing else -- was never
  // told, so an open markdown file kept the abandoned theme's baked token
  // colours against a code surface repainted for the new one, until some
  // unrelated body's completion bumped the version.
  scheduleVersionBump()
})

/** Visible for testing: drop all cached entries and in-flight tracking. */
export function _resetMarkdownCache(): void {
  markdownCache.clear()
  // Clear inFlight too: a text left "in flight" (its worker render never resolved)
  // would otherwise be skipped forever by the dedup guard, so a clear-and-retry could
  // never actually retry -- and a text dispatched in one test would leak into the next.
  inFlight.clear()
  retryCount.clear()
}

/** Visible for testing: number of cached entries. */
export function _getMarkdownCacheSize(): number {
  return markdownCache.size
}

/**
 * Return completed markdown HTML from the shared cache without subscribing to
 * worker-completion invalidations or dispatching a render. Selection-preserving
 * chat renders use this to keep already-displayed DOM stable.
 */
export function getCachedMarkdownHtml(text: string): string | undefined {
  return lruGet(markdownCache, text) // hit re-fronts to MRU end
}

/** Render `text` on this thread, where no Worker exists (see getSyncProcessor). */
function renderWithoutWorker(text: string): string {
  return renderWithPlainFallback(getSyncProcessor(), text)
}

/**
 * Start the off-thread render of `text` unless one is in flight, and return its
 * in-flight entry. Returns undefined when the render completed SYNCHRONOUSLY, which
 * happens only when the worker bridge throws: the source fallback is then cached.
 *
 * The caller has checked the cache and that a Worker exists.
 */
function ensureRender(text: string): InFlightRender | undefined {
  const existing = inFlight.get(text)
  if (existing !== undefined)
    return existing
  const render: InFlightRender = { urgent: false, isLowPriority: undefined }
  inFlight.set(text, render)
  let completedSynchronously = false
  // The theme this dispatch belongs to, captured once. The reply may land
  // after the user changed it, and a body highlighted under the abandoned
  // theme must neither be cached nor persisted -- the invalidator already
  // cleared the cache, so writing into it would restore exactly what the
  // clear removed and no later read would re-dispatch, because a cache hit
  // never does.
  const dispatchPair = syntaxThemePair()
  const dispatchNs = markdownArtifactNs()
  const complete = (result: MarkdownRenderResult | null): void => {
    if (!themeStillCurrent(dispatchPair)) {
      // The theme moved. Drop this result and let the invalidator's own
      // version bump drive the re-render under the current pair.
      //
      // WITHOUT TOUCHING `inFlight`. The invalidator already cleared it, and
      // a re-render under the new pair has since re-added this text, so
      // deleting here would drop a dispatch that is still outstanding and let
      // the next version bump dispatch the same body a third time.
      retryCount.delete(text)
      return
    }
    inFlight.delete(text)
    // A transient degrade (a grammar chunk load or the highlighter init failed): the
    // render is (partly) plain but a retry may recover it, so DON'T cache it. Bump the
    // version so a re-render re-dispatches -- bounded, so a grammar that never loads
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
      // Bound the map: entries are otherwise removed only on the final path below, so a
      // text that degrades retryably and whose reactive consumer then unmounts (scrolled
      // away before the version bump re-dispatches) would leak forever -- unlike the
      // LRU cache. Evicting the insertion-order-oldest entry at worst resets a long-idle
      // text's retry count, which just grants it the full budget again on a later re-render.
      capMapInsertionOrder(retryCount, CACHE_MAX_SIZE)
      scheduleVersionBump()
      return
    }
    retryCount.delete(text)
    // Inject the shared token-style rules BEFORE the HTML can render: the
    // worker minted the class names but has no document (see shikiStyleClass).
    if (result)
      ensureShikiStyleRules(result.styles)
    // A null result means no worker could render: it crashed, could not start,
    // rejected, or ran past the render's time budget. Show the escaped source rather
    // than parse on the main thread, and cache it, so the body does not re-dispatch
    // to a broken worker, or tie up a fresh one again, forever.
    lruSet(markdownCache, text, result?.html ?? markdownSourceFallbackHtml(text), CACHE_MAX_SIZE)
    scheduleVersionBump()
  }
  const dispatchToWorker = (): void => {
    try {
      renderMarkdownInWorker(text, dispatchPair, () => isLowPriorityRender(render))
        .then((result) => {
          // Persist only a CLEAN highlighted render for the reload warm-start:
          // a retryable degrade or a crash (null) is not a durable result.
          // The HTML length is guarded HERE rather than inside the shared
          // helper: it bounds the VALUE, and only this consumer has one whose
          // size is worth bounding apart from its key.
          if (result && !result.retryable && result.html.length <= PERSIST_MAX_HTML_LENGTH)
            markdownArtifact.write(dispatchNs, text, { h: result.html, s: result.styles })
          complete(result)
        })
        .catch(() => complete(null))
    }
    catch {
      complete(null)
      completedSynchronously = true
    }
  }
  // Reload warm-start: serve the persisted highlighted render when one exists,
  // else dispatch. Without a store this is a SYNCHRONOUS undefined, so the
  // dispatch keeps its same-frame timing.
  const persisted = markdownArtifact.read(text)
  if (persisted === undefined) {
    dispatchToWorker()
  }
  else {
    // The catch is load-bearing: a consumer waits for this render, and a chat row
    // stays hidden until it lands. A failed read must fall back to the worker
    // rather than leave the body in flight forever.
    void persisted
      .then((result) => {
        if (result !== undefined)
          complete(result)
        else
          dispatchToWorker()
      })
      .catch(() => dispatchToWorker())
  }
  return completedSynchronously
    ? undefined
    : render
}

/**
 * Return the final HTML of a Markdown body, or undefined while it renders.
 *
 * In the browser this NEVER parses Markdown on the main thread: a single
 * synchronous parse measured about 1.5 s for a 512 KB body in V8, and remark
 * recurses deeply enough to overflow the stack on nested input. A cache miss
 * dispatches the render to a worker and returns undefined; when the worker
 * answers, the result is cached and a version signal bumps, so a reactive caller
 * reads again and receives the HTML. The caller shows its pending state until
 * then, and a chat row stays hidden behind its loading skeleton.
 *
 * Without a Worker (unit tests / SSR) it renders synchronously and highlighted,
 * so the output is identical to the browser's eventual result and a caller is
 * never pending.
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
  markdownVersion()

  const cached = lruGet(markdownCache, text) // hit re-fronts to MRU end
  if (cached !== undefined)
    return cached

  if (!canUseWorker()) {
    const html = renderWithoutWorker(text)
    lruSet(markdownCache, text, html, CACHE_MAX_SIZE)
    return html
  }

  const render = ensureRender(text)
  if (render === undefined)
    return markdownCache.get(text)
  noteConsumerDemand(render, isLowPriority)
  return undefined
}

/**
 * Start rendering `text` at low priority, so a body that mounts later -- the body
 * of a collapsed section, when the reader expands it -- finds its HTML cached.
 * Subscribes to nothing and returns nothing. A consumer that requests the same
 * body before it lands raises its priority (see isLowPriorityRender).
 *
 * Without a Worker this does nothing: the consumer renders synchronously when it
 * mounts.
 */
export function prefetchMarkdownHtml(text: string): void {
  if (text === '' || !canUseWorker() || markdownCache.has(text))
    return
  ensureRender(text)
}
