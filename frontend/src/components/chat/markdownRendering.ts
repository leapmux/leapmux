import type { MarkdownRenderContext } from './renderContext'
import { getCachedMarkdownHtml, renderMarkdown, renderMarkdownCachedOrPlain, renderMarkdownPlain } from '~/lib/renderMarkdown'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { cachedRenderValueForString, getCachedRenderValueForString, setCachedRenderValueForString } from './messageRenderCache'
import { largeMarkdownPlainHtml, markdownNeedsPlainTextDisplay } from './safeTextDisplay'

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

function cachedHighlightedMarkdown(
  text: string,
  context: MarkdownRenderContext | undefined,
): string | undefined {
  const rowCached = getCachedRenderValueForString<string>(context, markdownCacheNamespace(), text)
  if (rowCached !== undefined)
    return rowCached
  const sharedCached = getCachedMarkdownHtml(text)
  return sharedCached === undefined ? undefined : setCachedRenderValueForString(context, markdownCacheNamespace(), text, sharedCached)
}

function rememberDisplayedMarkdown(
  context: MarkdownRenderContext | undefined,
  text: string,
  html: string,
): string {
  return setCachedRenderValueForString(context, 'markdown-displayed', text, html)
}

/** Render markdown without replacing selected text or stale theme colors. */
export function renderMarkdownForContext(text: string, context: MarkdownRenderContext | undefined): string {
  if (markdownNeedsPlainTextDisplay(text)) {
    return cachedRenderValueForString(
      context,
      'markdown-large-plain',
      text,
      () => largeMarkdownPlainHtml(text),
    )
  }
  if (context?.premeasureMode)
    return cachedRenderValueForString(context, 'markdown-plain', text, () => renderMarkdownPlain(text))
  if (isTextSelectionActive(context)) {
    const displayed = getCachedRenderValueForString<string>(context, 'markdown-displayed', text)
    if (displayed !== undefined)
      return displayed
    const highlighted = cachedHighlightedMarkdown(text, context)
    return rememberDisplayedMarkdown(
      context,
      text,
      highlighted ?? cachedRenderValueForString(context, 'markdown-plain', text, () => renderMarkdownPlain(text)),
    )
  }
  if (context?.syntaxHighlightingPaused?.()) {
    const highlighted = cachedHighlightedMarkdown(text, context)
    if (highlighted !== undefined)
      return rememberDisplayedMarkdown(context, text, highlighted)
    const html = renderMarkdownCachedOrPlain(text)
    const cached = getCachedMarkdownHtml(text)
    return rememberDisplayedMarkdown(
      context,
      text,
      cached === undefined ? html : setCachedRenderValueForString(context, markdownCacheNamespace(), text, cached),
    )
  }
  const rowCached = getCachedRenderValueForString<string>(context, markdownCacheNamespace(), text)
  if (rowCached !== undefined)
    return rememberDisplayedMarkdown(context, text, rowCached)
  const html = renderMarkdown(text, false, context?.rowOffscreen)
  const cached = getCachedMarkdownHtml(text)
  return rememberDisplayedMarkdown(
    context,
    text,
    cached === undefined ? html : setCachedRenderValueForString(context, markdownCacheNamespace(), text, cached),
  )
}
