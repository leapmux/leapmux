import { createRenderEffect } from 'solid-js'
import { lruSet } from './mapLru'

// ---------------------------------------------------------------------------
// Parsed-DOM fragment cache for rendered markdown/tool HTML
//
// The virtualized chat mounts a row ~4-5x as it scrolls in and out. The HTML
// string caches (renderMarkdown / the per-row render cache) already make the
// STRING cheap to re-obtain, but every remount still re-assigned it via
// `innerHTML`, forcing the browser to re-PARSE the same markup — 50-80KB of
// Shiki span soup for a code-heavy body — on each re-entry. This cache keeps
// the parse: each distinct HTML string is parsed ONCE into a detached
// <template>, and every (re)application deep-clones the template's content,
// which is several times cheaper than parsing for large bodies.
//
// Deliberately NOT used for the streaming tail: its HTML differs on every
// frame, so caching it would just churn parses into dead template entries.
// ---------------------------------------------------------------------------

/**
 * Entry bound: template subtrees are heavier than their source strings, so this
 * sits well below the 1024-entry HTML string cache while still covering several
 * viewports' worth of distinct bodies (visible + premeasure mounts share entries).
 */
const FRAGMENT_CACHE_MAX = 256

/**
 * Strings past this length are applied directly without caching the parsed
 * template: a single pathological body (a megabyte tool dump) would otherwise pin
 * a huge subtree in the cache for the marginal benefit of its own remounts.
 */
const FRAGMENT_CACHE_MAX_HTML_LENGTH = 256 * 1024

const cache = new Map<string, HTMLTemplateElement>()

/** Visible for testing. */
export function _resetFragmentCache(): void {
  cache.clear()
}

/** Visible for testing. */
export function _getFragmentCacheSize(): number {
  return cache.size
}

/**
 * Replace `el`'s children with the parsed form of `html`, parsing at most once
 * per distinct string (clone-on-hit thereafter). Equivalent to an `innerHTML`
 * assignment from the caller's point of view.
 */
export function applyCachedHtml(el: HTMLElement, html: string): void {
  if (html.length > FRAGMENT_CACHE_MAX_HTML_LENGTH) {
    el.innerHTML = html
    return
  }
  let tpl = cache.get(html)
  if (tpl === undefined) {
    tpl = document.createElement('template')
    tpl.innerHTML = html
  }
  // Shared LRU write (mapLru.lruSet): moves a hit to the most-recently-used end
  // (and inserts a miss there), then drops the insertion-order-oldest entries.
  lruSet(cache, html, tpl, FRAGMENT_CACHE_MAX)
  el.replaceChildren(tpl.content.cloneNode(true))
}

/**
 * Ref factory: bind an element's children to a reactive HTML string through the
 * fragment cache — the drop-in replacement for a JSX `innerHTML={html()}`
 * binding. Mirrors the compiled binding's equality skip: a re-evaluation that
 * yields the SAME string never touches the DOM (load-bearing for text-selection
 * stability, which relies on unchanged bodies keeping their exact nodes).
 *
 * Usage: `<div ref={cachedInnerHtml(() => html())} />`
 */
export function cachedInnerHtml(html: () => string): (el: HTMLElement) => void {
  return (el) => {
    let applied: string | undefined
    createRenderEffect(() => {
      const next = html()
      if (next === applied)
        return
      applied = next
      applyCachedHtml(el, next)
    })
  }
}
