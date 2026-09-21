import type { JSX } from 'solid-js'
import type { MarkdownRenderContext } from '../renderContext'
import { createMemo, Match, Show, Switch } from 'solid-js'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { containsAnsi, renderAnsi, stripAnsi } from '~/lib/renderAnsi'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { markdownContent } from '../markdownEditor/markdownContent.css'
import { getCachedRenderValueForString, setCachedRenderValueForString } from '../messageRenderCache'
import { renderMarkdownForContext, shouldPauseSyntaxHighlighting } from '../messageRenderers'
import { LIMITED_TEXT_DISPLAY_NOTICE, limitTextForDisplay } from '../safeTextDisplay'
import { JsonHighlightHtml } from '../syntaxHighlight'
import { toolResultCollapsed, toolResultContent, toolResultContentAnsi, toolResultContentPre, toolResultPrompt } from '../toolStyles.css'
import { canHighlightBySize } from './collapse'

/**
 * The kinds of content body that share the collapse-N-lines treatment:
 * - `'ansi-or-pre'`: pick `<ansi>` if the source contains ANSI escapes, else
 *   render as plain `<pre>` text. Used by command/task output.
 * - `'pre'`: always render as plain `<pre>`.
 * - `'markdown'`: render as markdown via `renderMarkdown` inside the
 *   `markdownContent` wrapper (used for assistant text).
 * - `'markdown-tool-result'`: render as markdown inside the `toolResultContent`
 *   wrapper (the styling used for WebFetch / Agent tool result bodies). A large
 *   value uses the shared limited plain-text display.
 * - `'json'`: JSON highlighted as token spans (via the async token worker)
 *   inside the shared `toolResultContentAnsi` wrapper. A large value uses the
 *   same display limits as plain text.
 */
export type CollapsibleContentKind = 'ansi-or-pre' | 'pre' | 'markdown' | 'markdown-tool-result' | 'json'

export interface CollapsibleContentProps {
  /**
   * Full body text — used for the ANSI detection in `'ansi-or-pre'` mode so
   * truncation never strips the escape sequence that would have flipped the
   * branch.
   */
  text: string
  /**
   * Display text — already truncated/sliced by the caller via `useCollapsedLines`.
   * Required for the slice-based kinds (`'pre'`, `'ansi-or-pre'`, `'markdown'`);
   * omit for `'markdown-tool-result'` and `'json'`, which derive their display
   * from `text`. When omitted, slice-based kinds also use `text`.
   */
  display?: string
  /** When true, applies the `toolResultCollapsed` fade class. */
  isCollapsed: boolean
  /** Body kind. See {@link CollapsibleContentKind}. */
  kind: CollapsibleContentKind
  /** Markdown/ANSI render capability; premeasure mode skips worker/Shiki work while preserving block layout. */
  context?: MarkdownRenderContext
}

/**
 * Render a tool-result body with the standard collapse-fade treatment.
 *
 * Pairs with `useCollapsedLines` — the hook computes `display` and
 * `isCollapsed`; this component picks the right base class and rendering
 * pipeline (ANSI / pre / markdown) and stitches the collapsed-class suffix.
 */
export function CollapsibleContent(props: CollapsibleContentProps): JSX.Element {
  const collapsedClass = () => props.isCollapsed ? ` ${toolResultCollapsed}` : ''
  const slice = () => props.display ?? props.text
  const safeDisplay = createMemo(() => limitTextForDisplay(slice()))
  const safeText = () => safeDisplay().text
  const rawDisplayLimited = () => props.kind !== 'markdown' && props.kind !== 'markdown-tool-result' && safeDisplay().limited
  const isAnsi = createMemo(() => props.kind === 'ansi-or-pre' && containsAnsi(props.text))
  const ansiPlainText = createMemo(() => isAnsi() ? stripAnsi(safeText()) : safeText())
  const pauseSyntax = () => shouldPauseSyntaxHighlighting(props.context)
  // The highlight namespace carries the syntax theme generation, for the same
  // reason `markdownCacheNamespace` does: `renderAnsi` bakes the resolved pair's
  // colours into the `sk-*` classes it mints, so an entry from the previous
  // theme is WRONG, not merely old. Reading the signal here is also what re-runs
  // the memo below -- nothing else in it moves when the theme does, so up to 512
  // live rows kept the abandoned theme's ANSI colours for the session.
  const ansiHighlightNs = () => `ansi-highlight:collapsibleContent:${syntaxThemeGeneration()}`
  const ansiHtml = (text: string) => {
    if (props.context?.premeasureMode)
      return undefined
    const displayed = getCachedRenderValueForString<string>(props.context, 'ansi-displayed:collapsibleContent', text)
    // Held (scroll pause or an active selection): keep whatever is on screen
    // rather than swapping text nodes under the user. It may carry the previous
    // theme for the length of the hold, and the next unheld pass repaints it --
    // the same trade `markdown-displayed` makes.
    if (pauseSyntax())
      return displayed
    const cached = getCachedRenderValueForString<string>(props.context, ansiHighlightNs(), text)
    if (cached !== undefined)
      return setCachedRenderValueForString(props.context, 'ansi-displayed:collapsibleContent', text, cached)
    // Over the size cap: never highlighted, so whatever is displayed is plain
    // and stays correct under any theme.
    if (!canHighlightBySize(text))
      return displayed
    const html = renderAnsi(text)
    setCachedRenderValueForString(props.context, ansiHighlightNs(), text, html)
    return setCachedRenderValueForString(props.context, 'ansi-displayed:collapsibleContent', text, html)
  }
  const renderedAnsiHtml = createMemo(() => isAnsi() ? ansiHtml(safeText()) : undefined)
  // Keep these as accessors. A Solid memo runs immediately and would parse both
  // Markdown forms for every plain, ANSI, and JSON body before Switch selects one.
  const markdownSliceHtml = () => renderMarkdownForContext(slice(), props.context)
  const markdownFullHtml = () => renderMarkdownForContext(props.text, props.context)
  const JsonContent = () => (
    <JsonHighlightHtml
      class={`${toolResultContentAnsi}${collapsedClass()}`}
      code={safeText()}
      {...(props.context !== undefined ? { context: props.context } : {})}
    />
  )

  return (
    <>
      <Switch>
        <Match when={props.kind === 'markdown'}>
          <div class={`${markdownContent}${collapsedClass()}`} ref={cachedInnerHtml(markdownSliceHtml)} />
        </Match>
        <Match when={props.kind === 'markdown-tool-result'}>
          {/* Normal Markdown bodies render in full. The shared Markdown guard changes
              an unsafe body to a limited plain-text display before parsing. */}
          <div class={`${toolResultContent}${collapsedClass()}`} ref={cachedInnerHtml(markdownFullHtml)} />
        </Match>
        <Match when={props.kind === 'json'}>
          {/* The token surface receives only the safe display text. */}
          <JsonContent />
        </Match>
        <Match when={props.kind === 'pre'}>
          <div class={`${toolResultContentPre}${collapsedClass()}`}>{safeText()}</div>
        </Match>
        <Match when={renderedAnsiHtml()}>
          {html => <div class={`${toolResultContentAnsi}${collapsedClass()}`} ref={cachedInnerHtml(html)} />}
        </Match>
        <Match when={props.kind === 'ansi-or-pre'}>
          <div class={`${toolResultContentPre}${collapsedClass()}`}>{ansiPlainText()}</div>
        </Match>
      </Switch>
      <Show when={!props.isCollapsed && rawDisplayLimited()}>
        <div class={toolResultPrompt}>{LIMITED_TEXT_DISPLAY_NOTICE}</div>
      </Show>
    </>
  )
}
