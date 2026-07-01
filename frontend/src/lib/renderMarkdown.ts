import type { MarkdownRenderResult } from './markdownWorkerClient'
import { createHighlighterCoreSync } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
// Eager grammars for the synchronous (no-Worker) fallback highlighter below. The
// async worker/editor paths lazy-load grammars instead, so these imports live here
// -- next to their only consumer -- rather than in the shared markdownProcessor.
import langBash from 'shiki/langs/bash.mjs'
import langC from 'shiki/langs/c.mjs'
import langCpp from 'shiki/langs/cpp.mjs'
import langCss from 'shiki/langs/css.mjs'
import langDiff from 'shiki/langs/diff.mjs'
import langGo from 'shiki/langs/go.mjs'
import langHtml from 'shiki/langs/html.mjs'
import langJava from 'shiki/langs/java.mjs'
import langJavascript from 'shiki/langs/javascript.mjs'
import langJson from 'shiki/langs/json.mjs'
import langJsx from 'shiki/langs/jsx.mjs'
import langMarkdown from 'shiki/langs/markdown.mjs'
import langPython from 'shiki/langs/python.mjs'
import langRust from 'shiki/langs/rust.mjs'
import langSql from 'shiki/langs/sql.mjs'
import langToml from 'shiki/langs/toml.mjs'
import langTsx from 'shiki/langs/tsx.mjs'
import langTypescript from 'shiki/langs/typescript.mjs'
import langXml from 'shiki/langs/xml.mjs'
import langYaml from 'shiki/langs/yaml.mjs'
import { createSignal } from 'solid-js'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { createMarkdownProcessor, plainMarkdownProcessor, renderWithPlainFallback } from './markdownProcessor'
import { renderMarkdownInWorker } from './markdownWorkerClient'
import { transparentBgThemes } from './shikiThemes'

/** The bundled Shiki language grammars the synchronous fallback highlighter pre-loads. */
const shikiLangs = [
  langTypescript,
  langJavascript,
  langPython,
  langRust,
  langGo,
  langJava,
  langBash,
  langJson,
  langHtml,
  langCss,
  langYaml,
  langToml,
  langSql,
  langMarkdown,
  langDiff,
  langC,
  langCpp,
  langJsx,
  langTsx,
  langXml,
]

// Synchronous Shiki highlighter, pre-loaded with the bundled languages. Still
// exported for the OTHER synchronous highlighting call sites (renderAnsi,
// ReadResultView, the markdown editor, tool renderers) that highlight short,
// bounded snippets where a worker round-trip would be overkill. Markdown bodies no
// longer use it on the hot path -- see renderMarkdown below.
export const shikiHighlighter = createHighlighterCoreSync({
  themes: transparentBgThemes,
  langs: shikiLangs,
  engine: createJavaScriptRegexEngine(),
})

// The full highlighted-markdown processor on the MAIN thread. Used only as the
// fallback when no Worker is available (unit tests / SSR) -- in the browser the
// worker renders instead, off the UI thread (see renderMarkdown).
const syncProcessor = createMarkdownProcessor(shikiHighlighter)

// LRU cache for rendered markdown HTML: avoids re-rendering identical content on
// re-mount (the virtualized chat list mounts a row ~4-5x as it scrolls in and out).
// Holds the HIGHLIGHTED result -- whether produced synchronously (fallback) or by
// the worker -- so a cache hit serves the final HTML with no flash. Sized well above
// a viewport's worth of distinct messages so a normal scroll session stops
// re-rendering rather than re-paying the worker round-trip after eviction.
const CACHE_MAX_SIZE = 1024
const markdownCache = new Map<string, string>()

// Plain (unhighlighted) PLACEHOLDER renders, cached separately from the highlighted
// markdownCache (a plain entry must not satisfy the markdownCache lookup, or the
// highlight would never be dispatched). The version signal bumps on every worker
// completion and re-evaluates EVERY on-screen markdown body; without this cache, each
// body still awaiting its highlight would re-run the synchronous plain remark render on
// every bump -- a thundering herd that measured ~3s cumulative across a scroll. Caching
// the placeholder makes those re-evals O(1). The entry is dropped once the highlighted
// result lands (the markdownCache hit serves it thereafter), so it never goes stale.
const placeholderCache = new Map<string, string>()

// Bumped whenever an async worker render completes and fills the cache, so a
// consumer that called renderMarkdown in a reactive context (every chat call site
// does, via a memo or a reactive `innerHTML`) re-renders and picks up the
// highlighted HTML in place of the plain placeholder it first received. Module-level
// so all markdown consumers share one invalidation; the memo/`innerHTML` equality
// check means only the rows whose HTML actually changed touch the DOM.
const [markdownVersion, setMarkdownVersion] = createSignal(0)
// Texts whose worker render is in flight, so concurrent/re-rendered consumers of the
// same body don't each dispatch a duplicate render.
const inFlight = new Set<string>()
// Per-text count of transient-failure retries (a grammar chunk load / highlighter init
// that failed and may recover). Bounds re-dispatch so a genuinely broken grammar
// eventually caches its plain render instead of re-dispatching forever -- mirrors the
// editor parser's MAX_LANG_LOAD_RETRIES budget, keeping the three Oniguruma consumers'
// recovery policy consistent.
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

// Whether off-thread rendering is available. False under unit tests / SSR (jsdom
// defines no Worker), where renderMarkdown falls back to a synchronous highlight so
// the rendered output is identical to the browser's eventual result.
function canUseWorker(): boolean {
  return typeof Worker !== 'undefined'
}

/** Visible for testing: drop all cached entries and in-flight tracking. */
export function _resetMarkdownCache(): void {
  markdownCache.clear()
  placeholderCache.clear()
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

/** Visible for testing: number of cached plain placeholders. */
export function _getPlaceholderCacheSize(): number {
  return placeholderCache.size
}

// Insert/refresh `key` in an LRU `map` (delete+set moves it to the most-recently-used
// end) and evict insertion-order-oldest entries past CACHE_MAX_SIZE. Shared by the
// highlighted and placeholder caches so their bound + eviction can't drift apart.
function lruSet(map: Map<string, string>, key: string, value: string): void {
  map.delete(key)
  map.set(key, value)
  capMapInsertionOrder(map, CACHE_MAX_SIZE)
}

/** Raw plain (no-Shiki) render, NOT cached -- for transient/streaming text that never repeats. */
function plainRender(text: string): string {
  return String(plainMarkdownProcessor.processSync(text))
}

/** Cached plain placeholder -- shown while the worker's highlighted result is in flight. */
function renderPlain(text: string): string {
  const cached = placeholderCache.get(text)
  if (cached !== undefined) {
    lruSet(placeholderCache, text, cached) // move to MRU end
    return cached
  }
  const result = plainRender(text)
  lruSet(placeholderCache, text, result)
  return result
}

/**
 * Render markdown without syntax highlighting or worker dispatch.
 *
 * Hidden chat premeasurement needs markdown block geometry (paragraph/list/code
 * structure) but must not enqueue Shiki work for rows the user may never see.
 * This shares the cached plain-placeholder path used by visible markdown while
 * highlighted HTML is still in flight.
 */
export function renderMarkdownPlain(text: string): string {
  return renderPlain(text)
}

/**
 * Return completed highlighted markdown from the shared cache without
 * subscribing to worker-completion invalidations. Selection-preserving chat
 * renders use this to keep already-highlighted DOM stable while refusing a
 * plain→highlighted swap that would clear the browser selection.
 */
export function getCachedMarkdownHtml(text: string): string | undefined {
  const cached = markdownCache.get(text)
  if (cached !== undefined)
    lruSet(markdownCache, text, cached)
  return cached
}

/**
 * Return highlighted markdown when it is already cached, otherwise return the
 * plain placeholder without dispatching a worker render. Used during scroll:
 * already-highlighted rows must not blink back to plain, but cache misses should
 * not start new syntax jobs on the scroll-critical path.
 */
export function renderMarkdownCachedOrPlain(text: string): string {
  const cached = getCachedMarkdownHtml(text)
  if (cached !== undefined)
    return cached
  return renderPlain(text)
}

/** Full synchronous highlighted render (main-thread Shiki). The no-Worker fallback. */
function renderHighlightedSync(text: string): string {
  return renderWithPlainFallback(syncProcessor, text)
}

/**
 * Render markdown to HTML.
 *
 * In the browser the expensive Shiki highlighting runs OFF the main thread: a
 * cache miss returns a fast plain (unhighlighted) placeholder immediately and
 * dispatches the highlight to a worker; when it lands, the result is cached and a
 * version signal bumps so the (reactive) caller re-renders with the highlighted
 * HTML in place. This keeps a large code-heavy body from blocking a frame -- a
 * single synchronous render measured up to ~1s on the main thread.
 *
 * Without a Worker (unit tests / SSR) it renders synchronously and highlighted, so
 * the output is identical to the browser's eventual result.
 *
 * `skipCache` (streaming / transient text) bypasses the cache entirely: in the
 * browser it returns an UNCACHED plain render without dispatching a worker render
 * (the text changes every frame, so highlighting it would thrash AND caching each
 * distinct frame would churn the placeholder cache the on-screen bodies rely on);
 * under tests it renders synchronously highlighted.
 */
export function renderMarkdown(text: string, skipCache = false): string {
  if (skipCache)
    return canUseWorker() ? plainRender(text) : renderHighlightedSync(text)

  // Subscribe to the version signal so an async worker completion re-renders this
  // (reactive) caller. Read BEFORE the cache lookup so the dependency is always
  // registered, including on the cache-hit path.
  markdownVersion()

  const cached = markdownCache.get(text)
  if (cached !== undefined) {
    lruSet(markdownCache, text, cached) // move to MRU end
    return cached
  }

  if (!canUseWorker()) {
    // No worker: render synchronously and cache.
    const html = renderHighlightedSync(text)
    lruSet(markdownCache, text, html)
    return html
  }

  // Dispatch the highlight off-thread (once per distinct text) and return the plain
  // placeholder now. On completion the highlighted HTML is cached and the version
  // bumps, re-rendering this caller with it. A null result (worker crash) caches the
  // plain render so it degrades gracefully instead of retrying forever.
  let completedSynchronously = false
  if (!inFlight.has(text)) {
    inFlight.add(text)
    const complete = (result: MarkdownRenderResult | null): void => {
      inFlight.delete(text)
      // A transient degrade (a grammar chunk load or the highlighter init failed): the
      // render is (partly) plain but a retry may recover it, so DON'T cache it. Bump the
      // version so a re-render re-dispatches -- bounded, so a grammar that never loads
      // still caches its plain render eventually instead of re-dispatching forever.
      if (result?.retryable && (retryCount.get(text) ?? 0) < MAX_MARKDOWN_RENDER_RETRIES) {
        // delete+set moves this actively-retrying text to the most-recently-used end BEFORE
        // capping. A bare set() on an existing key keeps its original insertion position, so
        // capMapInsertionOrder could evict the very entry still bouncing through the retry
        // loop -- resetting its count and re-granting the full budget. Moving it to MRU means
        // the cap evicts a genuinely idle entry instead, keeping the retry bound meaningful.
        const next = (retryCount.get(text) ?? 0) + 1
        retryCount.delete(text)
        retryCount.set(text, next)
        // Bound the map: entries are otherwise removed only on the terminal path below, so a
        // text that degrades retryably and whose reactive consumer then unmounts (scrolled
        // away before the version bump re-dispatches) would leak forever -- unlike the two
        // LRU caches. Evicting the insertion-order-oldest entry at worst resets a long-idle
        // text's retry count, which just grants it the full budget again on a later re-render.
        capMapInsertionOrder(retryCount, CACHE_MAX_SIZE)
        scheduleVersionBump()
        return
      }
      retryCount.delete(text)
      lruSet(markdownCache, text, result?.html ?? plainRender(text))
      // The highlighted (or fallback) result now serves from markdownCache, so the
      // plain placeholder for this text is dead -- drop it to bound the cache.
      placeholderCache.delete(text)
      scheduleVersionBump()
    }
    try {
      renderMarkdownInWorker(text)
        .then(complete)
        .catch(() => complete(null))
    }
    catch {
      complete(null)
      completedSynchronously = true
    }
  }
  return completedSynchronously ? markdownCache.get(text)! : renderPlain(text)
}
