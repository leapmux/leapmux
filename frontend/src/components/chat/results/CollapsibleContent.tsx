/* eslint-disable solid/no-innerhtml -- HTML is produced via renderAnsi/renderMarkdown, not arbitrary user input */
import type { JSX } from 'solid-js'
import { createMemo, Match, Switch } from 'solid-js'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from '../markdownEditor/markdownContent.css'
import { toolResultCollapsed, toolResultContent, toolResultContentAnsi, toolResultContentPre } from '../toolStyles.css'

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
 */
export type CollapsibleContentKind = 'ansi-or-pre' | 'pre' | 'markdown' | 'markdown-tool-result'

export interface CollapsibleContentProps {
  /**
   * Full body text — used for the ANSI detection in `'ansi-or-pre'` mode so
   * truncation never strips the escape sequence that would have flipped the
   * branch.
   */
  text: string
  /** Display text — already truncated/sliced by the caller via `useCollapsedLines`. */
  display: string
  /** When true, applies the `toolResultCollapsed` fade class. */
  isCollapsed: boolean
  /** Body kind. See {@link CollapsibleContentKind}. */
  kind: CollapsibleContentKind
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
  const isAnsi = createMemo(() => props.kind === 'ansi-or-pre' && containsAnsi(props.text))

  return (
    <Switch>
      <Match when={props.kind === 'markdown'}>
        <div class={`${markdownContent}${collapsedClass()}`} innerHTML={renderMarkdown(props.display)} />
      </Match>
      <Match when={props.kind === 'markdown-tool-result'}>
        {/* Markdown bodies don't truncate by lines (would slice mid-block); the
            full text is rendered and only the fade class differs by `isCollapsed`. */}
        <div class={`${toolResultContent}${collapsedClass()}`} innerHTML={renderMarkdown(props.text)} />
      </Match>
      <Match when={props.kind === 'pre'}>
        <div class={`${toolResultContentPre}${collapsedClass()}`}>{props.display}</div>
      </Match>
      <Match when={isAnsi()}>
        <div class={`${toolResultContentAnsi}${collapsedClass()}`} innerHTML={renderAnsi(props.display)} />
      </Match>
      <Match when={props.kind === 'ansi-or-pre'}>
        <div class={`${toolResultContentPre}${collapsedClass()}`}>{props.display}</div>
      </Match>
    </Switch>
  )
}
