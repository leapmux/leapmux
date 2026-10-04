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
      outputPreview
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
 * Select every tool kind whose result is ProseResult.
 * Derive the set from ToolResultByKind so type changes update the renderer factory.
 */
export type ProseKind = { [K in ToolKind]: ToolResultByKind[K] extends ProseResult ? K : never }[ToolKind]

/**
 * Build a renderer for a kind that returns prose.
 * The shared factory supplies its result body and toolbar metadata.
 * Each kind supplies its own icon, label, and title.
 * A kind can supply request content and an outcome title also.
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
