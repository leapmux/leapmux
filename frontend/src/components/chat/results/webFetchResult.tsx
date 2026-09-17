import type { JSX } from 'solid-js'
import type { FetchResult } from '../ir/tools/fetch'
import type { RenderContext } from '../messageRenderers'
import { Show } from 'solid-js'
import { formatBytes } from '~/lib/formatBytes'
import { getToolResultExpanded } from '../messageRenderers'
import { formatDuration, joinMetaParts } from '../rendererUtils'
import {
  toolMessage,
  toolResultPrompt,
} from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { useCollapsedFlag } from './useCollapsedLines'

export function WebFetchResultBody(props: {
  source: FetchResult
  context?: RenderContext
}): JSX.Element {
  const isCollapsed = useCollapsedFlag({
    text: () => props.source.result,
    expanded: () => getToolResultExpanded(props.context),
  })

  const summary = () => joinMetaParts([
    props.source.code !== undefined && `${props.source.code} ${props.source.codeText ?? ''}`.trim(),
    (props.source.bytes ?? 0) > 0 && formatBytes(props.source.bytes!),
    (props.source.durationMs ?? 0) > 0 && formatDuration(props.source.durationMs!),
  ])

  return (
    <div class={toolMessage}>
      <Show when={summary()}><div class={toolResultPrompt}>{summary()}</div></Show>
      <Show when={props.source.result}>
        <CollapsibleContent kind="markdown-tool-result" text={props.source.result} isCollapsed={isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </div>
  )
}
