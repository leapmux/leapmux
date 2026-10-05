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
 * Content kinds that share the collapsed-line display:
 * - ansi-or-pre renders ANSI spans when the source contains escapes. Otherwise, it renders plain text.
 * - pre renders plain text.
 * - markdown renders the selected Markdown text in the markdownContent element.
 * - markdown-tool-result renders tool Markdown with a limited plain-text fallback for large values.
 * - json renders token spans with the same display limits as plain text.
 */
export type CollapsibleContentKind = 'ansi-or-pre' | 'pre' | 'markdown' | 'markdown-tool-result' | 'json'

export interface CollapsibleContentProps {
  /**
   * When true, the displayed text element carries data-tool-output-preview.
   * Set it only for returned tool output. Never set it for arguments, headers, properties, or metadata.
   */
  outputPreview?: boolean
  /**
   * The original body text supplies ANSI detection.
   * A collapsed slice can omit the escape that identifies ANSI output.
   */
  text: string
  /**
   * The caller can supply a collapsed text slice.
   * The pre, ansi-or-pre, and markdown kinds use that slice.
   * Other kinds derive display text from the original text.
   * An absent slice uses the original text.
   */
  display?: string
  /** When true, applies the `toolResultCollapsed` fade class. */
  isCollapsed: boolean
  /** Body kind. See {@link CollapsibleContentKind}. */
  kind: CollapsibleContentKind
  /** The Markdown and ANSI render context. In premeasure mode, the component skips worker and Shiki work and keeps the block layout. */
  context?: MarkdownRenderContext
}

/**
 * Render tool text with the shared collapsed display style.
 * useCollapsedLines supplies the selected text and collapse state.
 * This component selects its format and display classes.
 */
export function CollapsibleContent(props: CollapsibleContentProps): JSX.Element {
  const collapsedClass = () => props.isCollapsed ? ` ${toolResultCollapsed}` : ''
  const outputPreviewAttribute = () => props.outputPreview ? '' : undefined
  const slice = () => props.display ?? props.text
  const safeDisplay = createMemo(() => limitTextForDisplay(slice()))
  const safeText = () => safeDisplay().text
  const rawDisplayLimited = () => props.kind !== 'markdown' && props.kind !== 'markdown-tool-result' && safeDisplay().limited
  const isAnsi = createMemo(() => props.kind === 'ansi-or-pre' && containsAnsi(props.text))
  const ansiPlainText = createMemo(() => isAnsi() ? stripAnsi(safeText()) : safeText())
  const pauseSyntax = () => shouldPauseSyntaxHighlighting(props.context)
  // Include the theme generation in the highlight cache key.
  // ANSI rendering stores theme colors in its token classes.
  // Reading the generation also updates each mounted row when the theme changes.
  const ansiHighlightNs = () => `ansi-highlight:collapsibleContent:${syntaxThemeGeneration()}`
  const ansiHtml = (text: string) => {
    if (props.context?.premeasureMode)
      return undefined
    const displayed = getCachedRenderValueForString<string>(props.context, 'ansi-displayed:collapsibleContent', text)
    // Keep the current text while scrolling or text selection pauses syntax updates.
    // The next unpaused read applies the current theme.
    if (pauseSyntax())
      return displayed
    const cached = getCachedRenderValueForString<string>(props.context, ansiHighlightNs(), text)
    if (cached !== undefined)
      return setCachedRenderValueForString(props.context, 'ansi-displayed:collapsibleContent', text, cached)
    // Values over the highlight limit keep their current plain-text display.
    // Plain text does not retain colors from an earlier theme.
    if (!canHighlightBySize(text))
      return displayed
    const html = renderAnsi(text)
    setCachedRenderValueForString(props.context, ansiHighlightNs(), text, html)
    return setCachedRenderValueForString(props.context, 'ansi-displayed:collapsibleContent', text, html)
  }
  const renderedAnsiHtml = createMemo(() => isAnsi() ? ansiHtml(safeText()) : undefined)
  // Keep both Markdown readers lazy.
  // A Solid memo would parse both forms before Switch selects the required format.
  const markdownSliceHtml = () => renderMarkdownForContext(slice(), props.context)
  const markdownFullHtml = () => renderMarkdownForContext(props.text, props.context)
  const JsonContent = () => (
    <JsonHighlightHtml
      class={`${toolResultContentAnsi}${collapsedClass()}`}
      code={safeText()}
      dataToolOutputPreview={props.outputPreview === true}
      {...(props.context !== undefined ? { context: props.context } : {})}
    />
  )

  return (
    <>
      <Switch>
        <Match when={props.kind === 'markdown'}>
          <div class={`${markdownContent}${collapsedClass()}`} data-tool-output-preview={outputPreviewAttribute()} ref={cachedInnerHtml(markdownSliceHtml)} />
        </Match>
        <Match when={props.kind === 'markdown-tool-result'}>
          {/* Normal Markdown bodies render in full. The shared Markdown guard changes
              an unsafe body to a limited plain-text display before parsing. */}
          <div class={`${toolResultContent}${collapsedClass()}`} data-tool-output-preview={outputPreviewAttribute()} ref={cachedInnerHtml(markdownFullHtml)} />
        </Match>
        <Match when={props.kind === 'json'}>
          {/* The token surface receives only the safe display text. */}
          <JsonContent />
        </Match>
        <Match when={props.kind === 'pre'}>
          <div class={`${toolResultContentPre}${collapsedClass()}`} data-tool-output-preview={outputPreviewAttribute()}>{safeText()}</div>
        </Match>
        <Match when={renderedAnsiHtml()}>
          {html => <div class={`${toolResultContentAnsi}${collapsedClass()}`} data-tool-output-preview={outputPreviewAttribute()} ref={cachedInnerHtml(html)} />}
        </Match>
        <Match when={props.kind === 'ansi-or-pre'}>
          <div class={`${toolResultContentPre}${collapsedClass()}`} data-tool-output-preview={outputPreviewAttribute()}>{ansiPlainText()}</div>
        </Match>
      </Switch>
      <Show when={!props.isCollapsed && rawDisplayLimited()}>
        <div class={toolResultPrompt}>{LIMITED_TEXT_DISPLAY_NOTICE}</div>
      </Show>
    </>
  )
}
