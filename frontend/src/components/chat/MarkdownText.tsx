import type { JSX } from 'solid-js'
import type { MarkdownRenderContext } from './renderContext'
import { createEffect, createMemo, Show } from 'solid-js'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { prefetchMarkdownHtml } from '~/lib/renderMarkdown'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { markdownHtmlForContext, rowCachedMarkdownHtml } from './markdownRendering'
import { useRenderReadiness } from './renderReadiness'
import { SKELETON_LINE_PITCH_PX, SkeletonLines } from './widgets/ChatRowSkeleton'

/** The most skeleton lines that a pending Markdown body shows. */
const MARKDOWN_PENDING_MAX_LINES = 6

/** The number of lines in `text`, counted up to `max`. Reads no further than the last counted newline. */
function lineCountUpTo(text: string, max: number): number {
  let lines = 1
  let index = text.indexOf('\n')
  while (index !== -1 && lines < max) {
    lines++
    index = text.indexOf('\n', index + 1)
  }
  return lines
}

/**
 * The placeholder of a Markdown body outside the chat while the worker renders it:
 * one shimmer line for each source line, up to {@link MARKDOWN_PENDING_MAX_LINES}.
 */
function MarkdownPendingSkeleton(props: { text: string }): JSX.Element {
  const height = () => lineCountUpTo(props.text, MARKDOWN_PENDING_MAX_LINES) * SKELETON_LINE_PITCH_PX
  return <SkeletonLines height={height()} seed="markdown-pending" testId="markdown-pending" />
}

/**
 * Render a Markdown body through the shared remark pipeline. The HTML is produced
 * via remark + sanitizer, never arbitrary user input. It is applied through the
 * parsed-fragment cache (~/lib/htmlFragmentCache) rather than an `innerHTML`
 * binding, so a re-mounting row clones the already-parsed template instead of
 * making the browser re-parse the same markup.
 *
 * The HTML comes from a worker, so the body can mount before it exists. Until then:
 * - Inside a chat row, it reports itself pending to the row's readiness scope
 *   (`context.readiness`) and draws nothing. The row stays hidden behind its own
 *   skeleton, with its height uncommitted, until every body is ready.
 * - Elsewhere, it shows a skeleton of its own.
 */
export function MarkdownText(props: {
  text: string
  context?: MarkdownRenderContext | undefined
  /** The class of the body element. Defaults to `markdownContent`. */
  class?: string
  /** Mark the body as returned tool output; see CollapsibleContentProps.outputPreview. */
  outputPreview?: boolean
}): JSX.Element {
  // The HTML this body shows, and the text it renders. markdownHtmlForContext keeps
  // it while the reader selects or scrolls, and while a theme change renders the SAME
  // text again, so the body keeps its old colours instead of turning into a skeleton.
  let shown: { text: string, html: string } | undefined
  // A memo of its own, so that the lookup, which hashes the whole text, does not run
  // again at each worker completion that re-runs the memo below. The memo does not see
  // a later write into the row cache, and it does not need to: every write stores HTML
  // that the shared cache held first, and the memo below reads that cache next.
  const rowCached = createMemo(() => rowCachedMarkdownHtml(props.text, props.context))
  const html = createMemo(() => {
    const text = props.text
    const next = markdownHtmlForContext(text, props.context, {
      displayed: shown?.text === text
        ? shown.html
        : undefined,
      rowCached: rowCached(),
    })
    if (next !== undefined)
      shown = { text, html: next }
    return next
  })
  const pending = () => html() === undefined
  const drawsOwnSkeleton = useRenderReadiness(() => props.context, pending)
  return (
    <Show
      when={!pending()}
      fallback={drawsOwnSkeleton
        ? <MarkdownPendingSkeleton text={props.text} />
        : null}
    >
      <div
        class={props.class ?? markdownContent}
        data-tool-output-preview={props.outputPreview === true
          ? ''
          : undefined}
        ref={cachedInnerHtml(() => html() ?? '')}
      />
    </Show>
  )
}

/**
 * Render nothing, but start rendering `text` at low priority. Place it where a
 * collapsed section hides a Markdown body, so that expanding the section finds the
 * HTML cached: the row then re-measures at once instead of waiting for the worker.
 *
 * It prefetches again after a syntax theme change, which empties the HTML cache.
 * The hidden premeasure copy of a row prefetches nothing: the reader expands only
 * the visible copy, which prefetches the same body when it mounts. The idle
 * warm-up mounts a premeasure copy of each unmeasured row in the transcript, so a
 * prefetch there would render and store every collapsed body, mostly text that the
 * reader never opens.
 */
export function MarkdownPrefetch(props: { text: string, context?: MarkdownRenderContext | undefined }): JSX.Element {
  createEffect(() => {
    syntaxThemeGeneration()
    if (props.context?.premeasureMode !== true)
      prefetchMarkdownHtml(props.text)
  })
  return null
}
