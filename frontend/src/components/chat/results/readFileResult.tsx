import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ParsedCatLine } from './ReadResultView'
import { Show } from 'solid-js'
import { COLLAPSED_RESULT_ROWS } from '../toolRenderers'
import { toolMessage, toolResultCollapsed, toolResultContentPre } from '../toolStyles.css'
import { ReadResultView } from './ReadResultView'

/**
 * Provider-neutral source for a Read tool result. `lines` is null for raw
 * text that doesn't parse as cat-n format (or non-text Read variants on
 * Claude — image/notebook/pdf/parts/file_unchanged); the body falls back to
 * `fallbackContent` in that case.
 */
export interface ReadFileResultSource {
  filePath: string
  /** Pre-parsed cat-n lines, or null when unparseable / non-text. */
  lines: ParsedCatLine[] | null
  /** Total file lines (Claude tool_use_result.file.totalLines). 0 when unknown. */
  totalLines: number
  /** Returned lines count from Claude tool_use_result.file. 0 when unknown. */
  numLines: number
  /** Raw fallback content used when `lines` is null. */
  fallbackContent: string
}

export function ReadFileResultBody(props: {
  source: ReadFileResultSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded?.() ?? false
  const lines = () => props.source.lines
  const isCollapsed = () => {
    const ls = lines()
    if (!ls)
      return false
    return !expanded() && ls.length > COLLAPSED_RESULT_ROWS
  }
  const displayLines = () => {
    const ls = lines()
    if (!ls)
      return []
    if (expanded() || ls.length <= COLLAPSED_RESULT_ROWS)
      return ls
    return ls.slice(0, COLLAPSED_RESULT_ROWS)
  }

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show
        when={lines() && lines()!.length > 0}
        fallback={<div class={toolResultContentPre}>{props.source.fallbackContent || 'Empty file'}</div>}
      >
        <ReadResultView lines={displayLines()} filePath={props.source.filePath} />
      </Show>
    </div>
  )
}
