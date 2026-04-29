import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { Show } from 'solid-js'
import { formatBytes } from '~/lib/formatBytes'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { getToolResultExpanded } from '../messageRenderers'
import { formatDuration, joinMetaParts } from '../rendererUtils'
import {
  toolMessage,
  toolResultPrompt,
} from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { useCollapsedFlag } from './useCollapsedLines'

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

/**
 * Build a WebFetchResultSource from a record carrying `{code, codeText, bytes,
 * durationMs, result, url}`. Returns null when `code` is not a number — the
 * caller can then fall back to the generic text branch.
 */
export function webFetchFromObj(
  obj: Record<string, unknown> | null | undefined,
  opts?: { resultFallback?: string },
): WebFetchResultSource | null {
  if (!obj || typeof obj.code !== 'number')
    return null
  return {
    code: obj.code,
    codeText: pickString(obj, 'codeText'),
    bytes: pickNumber(obj, 'bytes', 0),
    durationMs: pickNumber(obj, 'durationMs', 0),
    result: pickString(obj, 'result', opts?.resultFallback ?? ''),
    url: pickString(obj, 'url', undefined),
  }
}

export function WebFetchResultBody(props: {
  source: WebFetchResultSource
  context?: RenderContext
}): JSX.Element {
  const isCollapsed = useCollapsedFlag({
    text: () => props.source.result,
    expanded: () => getToolResultExpanded(props.context),
  })

  const summary = () => joinMetaParts([
    `${props.source.code} ${props.source.codeText}`,
    props.source.bytes > 0 && formatBytes(props.source.bytes),
    props.source.durationMs > 0 && formatDuration(props.source.durationMs),
  ])

  return (
    <div class={toolMessage}>
      <div class={toolResultPrompt}>{summary()}</div>
      <Show when={props.source.result}>
        <CollapsibleContent kind="markdown-tool-result" text={props.source.result} isCollapsed={isCollapsed()} />
      </Show>
    </div>
  )
}
