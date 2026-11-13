import type { MarkdownRenderContext } from './renderContext'
import { getCachedMarkdownHtml, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'
import { isDisplayHeld } from './renderContext'

/**
 * The longest HTML, in characters, that the row cache keeps for one body. A
 * longer render stays only in the shared cache, which limits the characters that
 * it holds in total (see MARKDOWN_CACHE_MAX_CHARS in ~/lib/renderMarkdown). The
 * row cache keeps a row's entries for as long as the row stays among the recent
 * ones, so a large render there would outlive the shared cache's eviction and
 * defeat that limit. A remount of such a body after an eviction renders it again.
 * The limit matches the largest HTML that the parsed-fragment cache keeps.
 */
export const MAX_ROW_CACHED_MARKDOWN_HTML_CHARS = 256 * 1024

/** Store `html` in the row cache unless it is too large to keep there, and return it. */
function rowCacheMarkdownHtml(context: MarkdownRenderContext | undefined, text: string, html: string): string {
  if (html.length > MAX_ROW_CACHED_MARKDOWN_HTML_CHARS)
    return html
  return setCachedRenderValueForString(context, markdownCacheNamespace(), text, html)
}

/** Return the per-row cache namespace for the current syntax theme. */
export function markdownCacheNamespace(): string {
  return `markdown-html:${syntaxThemeGeneration()}`
}

/**
 * Return the HTML of `text` under the current theme from the row cache, or
 * undefined when the row holds none.
 *
 * The lookup hashes the whole text (see stringCacheKey), so a body reads it in a
 * memo of its own. That memo re-runs only when the text, the theme or the row
 * changes. The memo that calls {@link markdownHtmlForContext} re-runs at each
 * completion of ANY body while this body waits for the worker, and a lookup there
 * hashed the text again at each one.
 */
export function rowCachedMarkdownHtml(text: string, context: MarkdownRenderContext | undefined): string | undefined {
  return getCachedRenderValueForString<string>(context, markdownCacheNamespace(), text)
}

/** What a body already holds for its text, for {@link markdownHtmlForContext}. */
export interface MarkdownBodyHtml {
  /** The HTML that the body shows now for the same text, if any. */
  displayed: string | undefined
  /** The row cache's HTML of the same text under the current theme: {@link rowCachedMarkdownHtml}. */
  rowCached: string | undefined
}

/**
 * Return the HTML that a chat body shows for `text`, or undefined while the worker
 * renders it. The main thread never parses Markdown here (see requestMarkdownHtml).
 *
 * The body keeps the HTML that it displays in two cases:
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
  { displayed, rowCached }: MarkdownBodyHtml,
): string | undefined {
  if (displayed !== undefined && isDisplayHeld(context))
    return displayed
  if (rowCached !== undefined)
    return rowCached
  const shared = getCachedMarkdownHtml(text)
  if (shared !== undefined)
    return rowCacheMarkdownHtml(context, text, shared)
  const requested = requestMarkdownHtml(text, context?.rowOffscreen)
  if (requested !== undefined)
    return rowCacheMarkdownHtml(context, text, requested)
  return displayed
}
