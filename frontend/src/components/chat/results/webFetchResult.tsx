import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { Show } from 'solid-js'
import { formatBytes } from '~/lib/formatBytes'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { getToolResultExpanded } from '../messageRenderers'
import { formatDuration } from '../rendererUtils'
import {
  toolMessage,
  toolResultPrompt,
} from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { useCollapsedLines } from './useCollapsedLines'

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
  const expanded = () => getToolResultExpanded(props.context)
  const text = () => props.source.result
  const { display, isCollapsed } = useCollapsedLines({ text, expanded })

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
        <CollapsibleContent kind="markdown-tool-result" text={text()} display={display()} isCollapsed={isCollapsed()} />
      </Show>
    </div>
  )
}
