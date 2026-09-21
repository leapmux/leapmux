import type { JSX } from 'solid-js'
import type { ProseResult } from '../../model/toolCall'
import type { ToolKind } from '../../model/toolKind'
import type { ToolResultByKind } from '../../model/tools'
import type { ToolKindMeta, ToolKindRenderer, ToolRowView } from './renderer'
import { toolInputSummary } from '../../toolStyles.css'
import { CollapsibleContent } from '../CollapsibleContent'
import { textNeedsCollapse, useCollapsedLines } from '../useCollapsedLines'

/** The body a prose result draws: markdown when the words are markdown, a plain block otherwise. */
export function ProseResultBody(props: { result: ProseResult, view: ToolRowView }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.result.text, expanded: () => props.view.expanded() })
  return (
    <CollapsibleContent
      kind={props.result.format === 'markdown' ? 'markdown-tool-result' : 'pre'}
      text={props.result.text}
      display={collapsed.display()}
      isCollapsed={collapsed.isCollapsed()}
      {...(props.view.context !== undefined ? { context: props.view.context } : {})}
    />
  )
}

/** The meta a prose result offers: copyable words, collapsible when long. */
export function proseMeta(result: ProseResult): ToolKindMeta {
  return {
    collapsible: textNeedsCollapse(result.text),
    hasDiff: false,
    copyableContent: () => result.text || null,
  }
}

/**
 * Every kind whose result IS prose.
 *
 * Derived from `ToolResultByKind` rather than listed, so a kind that stops answering
 * with words -- or a new one that starts -- moves in and out of the factory below
 * by the declaration alone.
 */
export type ProseKind = { [K in ToolKind]: ToolResultByKind[K] extends ProseResult ? K : never }[ToolKind]

/**
 * The renderer a prose kind gets for stating its icon, its label and its title.
 *
 * Eight kinds answer with words and nothing else, so the body and the toolbar are
 * the same two lines for each. They live here once: a kind states what differs,
 * and adds a `request` or an `outcomeTitle` of its own when it has one.
 */
export function proseRenderer<K extends ProseKind>(base: Omit<ToolKindRenderer<K>, 'result' | 'resultMeta'>): ToolKindRenderer<K> {
  return {
    ...base,
    result(call, view) {
      return <ProseResultBody result={call.result as ProseResult} view={view} />
    },
    resultMeta(call) {
      return proseMeta(call.result as ProseResult)
    },
  }
}

/** One line of typed request facts, drawn while the call runs and the answer has not landed. */
export function typedRequestLine(parts: Array<string | undefined>): JSX.Element | null {
  const line = parts.filter(Boolean).join(' · ')
  return line ? <div class={toolInputSummary}>{line}</div> : null
}
