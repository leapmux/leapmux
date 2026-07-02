import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { createMemo, Match, Switch } from 'solid-js'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { containsAnsi, renderAnsi, stripAnsi } from '~/lib/renderAnsi'
import { markdownContent } from '../markdownEditor/markdownContent.css'
import { getCachedRenderValueForString, setCachedRenderValueForString } from '../messageRenderCache'
import { renderMarkdownForContext, shouldPauseSyntaxHighlighting } from '../messageRenderers'
import { JsonHighlightHtml } from '../toolRenderers'
import { toolResultCollapsed, toolResultContent, toolResultContentAnsi, toolResultContentPre } from '../toolStyles.css'
import { canHighlightBySize } from './collapse'

/**
 * The kinds of content body that share the collapse-N-lines treatment:
 * - `'ansi-or-pre'`: pick `<ansi>` if the source contains ANSI escapes, else
 *   render as plain `<pre>` text. Used by command/task output.
 * - `'pre'`: always render as plain `<pre>`.
 * - `'markdown'`: render as markdown via `renderMarkdown` inside the
 *   `markdownContent` wrapper (used for assistant text).
 * - `'markdown-tool-result'`: render as markdown inside the `toolResultContent`
 *   wrapper (the styling used for WebFetch / Agent tool result bodies). The
 *   full text is always rendered; only the fade class differs.
 * - `'json'`: JSON highlighted as token spans (via the async token worker)
 *   inside the shared `toolResultContentAnsi` wrapper. Like the markdown
 *   variants, the full text is always rendered (slicing mid-token would break
 *   the output); only the fade class differs.
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
   * omit for `'markdown-tool-result'` and `'json'`, which always render the
   * full `text` and only flip the fade class. When omitted, slice-based kinds
   * fall back to rendering `text` in full.
   */
  display?: string
  /** When true, applies the `toolResultCollapsed` fade class. */
  isCollapsed: boolean
  /** Body kind. See {@link CollapsibleContentKind}. */
  kind: CollapsibleContentKind
  /** Renderer context; premeasure mode skips worker/Shiki work while preserving block layout. */
  context?: RenderContext
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
  const isAnsi = createMemo(() => props.kind === 'ansi-or-pre' && containsAnsi(props.text))
  const ansiPlainText = createMemo(() => isAnsi() ? stripAnsi(slice()) : slice())
  const pauseSyntax = () => shouldPauseSyntaxHighlighting(props.context)
  const ansiHtml = (text: string) => {
    if (props.context?.premeasureMode)
      return undefined
    const displayed = getCachedRenderValueForString<string>(props.context, 'ansi-displayed:collapsibleContent', text)
    if (displayed !== undefined)
      return displayed
    if (pauseSyntax() || !canHighlightBySize(text))
      return undefined
    const cached = getCachedRenderValueForString<string>(props.context, 'ansi-highlight:collapsibleContent', text)
    if (cached !== undefined)
      return setCachedRenderValueForString(props.context, 'ansi-displayed:collapsibleContent', text, cached)
    const html = renderAnsi(text)
    setCachedRenderValueForString(props.context, 'ansi-highlight:collapsibleContent', text, html)
    return setCachedRenderValueForString(props.context, 'ansi-displayed:collapsibleContent', text, html)
  }
  const renderedAnsiHtml = createMemo(() => isAnsi() ? ansiHtml(slice()) : undefined)
  const markdownHtml = (text: string) => renderMarkdownForContext(text, props.context)
  const markdownSliceHtml = createMemo(() => markdownHtml(slice()))
  const markdownFullHtml = createMemo(() => markdownHtml(props.text))
  const JsonContent = () => (
    <JsonHighlightHtml
      class={`${toolResultContentAnsi}${collapsedClass()}`}
      code={props.text}
      context={props.context}
    />
  )

  return (
    <Switch>
      <Match when={props.kind === 'markdown'}>
        <div class={`${markdownContent}${collapsedClass()}`} ref={cachedInnerHtml(markdownSliceHtml)} />
      </Match>
      <Match when={props.kind === 'markdown-tool-result'}>
        {/* Markdown bodies don't truncate by lines (would slice mid-block); the
            full text is rendered and only the fade class differs by `isCollapsed`. */}
        <div class={`${toolResultContent}${collapsedClass()}`} ref={cachedInnerHtml(markdownFullHtml)} />
      </Match>
      <Match when={props.kind === 'json'}>
        {/* Same as 'markdown-tool-result': render full shiki HTML; visual clip via fade. */}
        <JsonContent />
      </Match>
      <Match when={props.kind === 'pre'}>
        <div class={`${toolResultContentPre}${collapsedClass()}`}>{slice()}</div>
      </Match>
      <Match when={renderedAnsiHtml()}>
        {html => <div class={`${toolResultContentAnsi}${collapsedClass()}`} ref={cachedInnerHtml(html)} />}
      </Match>
      <Match when={props.kind === 'ansi-or-pre'}>
        <div class={`${toolResultContentPre}${collapsedClass()}`}>{ansiPlainText()}</div>
      </Match>
    </Switch>
  )
}
