import type { MarkdownRenderResult } from './markdownWorkerClient'
import themeGithubDark from '@shikijs/themes/github-dark'
import themeGithubLight from '@shikijs/themes/github-light'
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
import { capMapInsertionOrder, lruGet, lruSet } from '~/lib/mapLru'
import { createMarkdownProcessor, plainMarkdownProcessor, renderWithPlainFallback } from './markdownProcessor'
import { renderMarkdownInWorker } from './markdownWorkerClient'
import { createPersistedArtifact } from './persistedArtifact'
import { ensureShikiStyleRules } from './shikiStyleClass'
import { createPairKeyedCache, syntaxThemePair, themeStillCurrent } from './shikiThemes'
import { withTransparentBg } from './syntaxThemes'
import { onSyntaxThemeChange } from './syntaxThemeStore'

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
// The initial pair is the Default theme's, which is GitHub's, imported
// STATICALLY: a synchronous highlighter cannot await a chunk, so the one pair
// the app starts on has to sit in the main bundle. Every other pair arrives
// lazily -- `setSyntaxTheme` registers it here before pointing the shared
// options at it, so a synchronous call site never names an unregistered theme.
export const shikiHighlighter = createHighlighterCoreSync({
  themes: [withTransparentBg(themeGithubLight), withTransparentBg(themeGithubDark)],
  langs: shikiLangs,
  engine: createJavaScriptRegexEngine(),
})

// The full highlighted-markdown processor on the MAIN thread. Used only as the
// fallback when no Worker is available (unit tests / SSR) -- in the browser the
// worker renders instead, off the UI thread (see renderMarkdown).
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
  placeholderCache.clear()
  // `inFlight` and `retryCount` go with them, exactly as `_resetMarkdownCache`
  // does. A body still rendering under the OLD pair is skipped by the
  // `inFlight.has(text)` dedup guard, so the re-render after a theme change
  // returned the plain placeholder and never re-dispatched -- and when the old
  // reply landed it wrote the abandoned theme's HTML into the cache this line
  // just cleared and bumped the version, so the row repainted in the theme the
  // user had just left while its neighbours moved on.
  inFlight.clear()
  retryCount.clear()
  // Then TELL the consumers, the way every other mutation of these caches does.
  // A chat row happens to re-render anyway, because its own cache namespace
  // folds in `syntaxThemeGeneration()`. A consumer that reads only
  // `markdownVersion()` -- `MarkdownFileView`, whose memo depends on the file
  // text and nothing else -- was never told, so an open markdown file kept the
  // abandoned theme's baked token colours against a code surface repainted for
  // the new one, until some unrelated body's completion bumped the version.
  scheduleVersionBump()
})

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

/** Raw plain (no-Shiki) render, NOT cached -- for transient/streaming text that never repeats. */
function plainRender(text: string): string {
  return String(plainMarkdownProcessor.processSync(text))
}

/** Cached plain placeholder -- shown while the worker's highlighted result is in flight. */
function renderPlain(text: string): string {
  const cached = lruGet(placeholderCache, text) // hit re-fronts to MRU end
  if (cached !== undefined)
    return cached
  const result = plainRender(text)
  lruSet(placeholderCache, text, result, CACHE_MAX_SIZE)
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
  return lruGet(markdownCache, text) // hit re-fronts to MRU end
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
  return renderWithPlainFallback(getSyncProcessor(), text)
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
 *
 * `isLowPriority` (re-read at each dispatch opportunity) orders this body's
 * worker render behind currently-high ones — rows outside the near-viewport
 * band pass it so viewport rows upgrade first (see createWorkerPriorityGate).
 */
export function renderMarkdown(text: string, skipCache = false, isLowPriority?: () => boolean): string {
  if (skipCache)
    return canUseWorker() ? plainRender(text) : renderHighlightedSync(text)

  // Subscribe to the version signal so an async worker completion re-renders this
  // (reactive) caller. Read BEFORE the cache lookup so the dependency is always
  // registered, including on the cache-hit path.
  markdownVersion()

  const cached = lruGet(markdownCache, text) // hit re-fronts to MRU end
  if (cached !== undefined)
    return cached

  if (!canUseWorker()) {
    // No worker: render synchronously and cache.
    const html = renderHighlightedSync(text)
    lruSet(markdownCache, text, html, CACHE_MAX_SIZE)
    return html
  }

  // Dispatch the highlight off-thread (once per distinct text) and return the plain
  // placeholder now. On completion the highlighted HTML is cached and the version
  // bumps, re-rendering this caller with it. A null result (worker crash) caches the
  // plain render so it degrades gracefully instead of retrying forever.
  let completedSynchronously = false
  if (!inFlight.has(text)) {
    inFlight.add(text)
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
      // Inject the shared token-style rules BEFORE the HTML can render: the
      // worker minted the class names but has no document (see shikiStyleClass).
      if (result)
        ensureShikiStyleRules(result.styles)
      lruSet(markdownCache, text, result?.html ?? plainRender(text), CACHE_MAX_SIZE)
      // The highlighted (or fallback) result now serves from markdownCache, so the
      // plain placeholder for this text is dead -- drop it to bound the cache.
      placeholderCache.delete(text)
      scheduleVersionBump()
    }
    const dispatchToWorker = (): void => {
      try {
        renderMarkdownInWorker(text, dispatchPair, isLowPriority)
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
      void persisted.then((result) => {
        if (result !== undefined)
          complete(result)
        else
          dispatchToWorker()
      })
    }
  }
  return completedSynchronously ? markdownCache.get(text)! : renderPlain(text)
}
