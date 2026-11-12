import type { MarkdownRenderContext } from './renderContext'
import { getCachedMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'

/** Return the per-row cache namespace for the current syntax theme. */
export function markdownCacheNamespace(): string {
  return `markdown-html:${syntaxThemeGeneration()}`
}

/** Return true when a renderer must keep the displayed syntax stable. */
export function shouldPauseSyntaxHighlighting(context: MarkdownRenderContext | undefined): boolean {
  return context?.premeasureMode === true || context?.syntaxHighlightingPaused?.() === true || isTextSelectionActive(context)
}

function isTextSelectionActive(context: MarkdownRenderContext | undefined): boolean {
  return context?.textSelectionActive?.() === true
}

/** The HTML of `text` under the CURRENT theme, from the row cache or the shared cache. */
function cachedCurrentThemeHtml(
  text: string,
  context: MarkdownRenderContext | undefined,
): string | undefined {
  const rowCached = getCachedRenderValueForString<string>(context, markdownCacheNamespace(), text)
  if (rowCached !== undefined)
    return rowCached
  const sharedCached = getCachedMarkdownHtml(text)
  return sharedCached === undefined
    ? undefined
    : setCachedRenderValueForString(context, markdownCacheNamespace(), text, sharedCached)
}

/**
 * Return the HTML that a chat body shows for `text`, or undefined while the worker
 * renders it. The main thread never parses Markdown here (see requestMarkdownHtml).
 *
 * `displayed` is the HTML that the body shows now for the same text, if any. The
 * body keeps it in two cases:
 * - While the reader selects text or the view scrolls, so the DOM under the
 *   selection or the scroll anchor does not change.
 * - While a theme change renders the new HTML, so the body keeps its old colours
 *   instead of disappearing. A theme changes colours only, never geometry.
 *
 * A body that shows nothing yet dispatches its render in every case, the hidden
 * premeasure pass included: premeasure is where a row completes its Markdown, and
 * the row stays hidden until it has.
 */
export function markdownHtmlForContext(
  text: string,
  context: MarkdownRenderContext | undefined,
  displayed: string | undefined,
): string | undefined {
  if (displayed !== undefined && (isTextSelectionActive(context) || context?.syntaxHighlightingPaused?.() === true))
    return displayed
  const current = cachedCurrentThemeHtml(text, context)
  if (current !== undefined)
    return current
  const requested = requestMarkdownHtml(text, context?.rowOffscreen)
  if (requested !== undefined)
    return setCachedRenderValueForString(context, markdownCacheNamespace(), text, requested)
  return displayed
}
