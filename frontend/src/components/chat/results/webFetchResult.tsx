/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { Show } from 'solid-js'
import { formatBytes } from '~/lib/formatBytes'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { formatDuration } from '../rendererUtils'
import { COLLAPSED_RESULT_ROWS } from '../toolRenderers'
import {
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultPrompt,
} from '../toolStyles.css'

/** Provider-neutral source for a WebFetch tool result. */
export interface WebFetchResultSource {
  code: number
  codeText: string
  bytes: number
  durationMs: number
  /** Markdown body returned by the fetch. */
  result: string
  /** Post-redirect URL (Claude tool_use_result.url). */
  url?: string
}

export function WebFetchResultBody(props: {
  source: WebFetchResultSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded?.() ?? false
  const isCollapsed = () => !expanded() && props.source.result.split('\n').length > COLLAPSED_RESULT_ROWS

  const summary = () => {
    const parts: string[] = []
    parts.push(`${props.source.code} ${props.source.codeText}`)
    if (props.source.bytes > 0)
      parts.push(formatBytes(props.source.bytes))
    if (props.source.durationMs > 0)
      parts.push(formatDuration(props.source.durationMs))
    return parts.join(' · ')
  }

  return (
    <div class={toolMessage}>
      <div class={toolResultPrompt}>{summary()}</div>
      <Show when={props.source.result}>
        <div
          class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}
          innerHTML={renderMarkdown(props.source.result)}
        />
      </Show>
    </div>
  )
}
