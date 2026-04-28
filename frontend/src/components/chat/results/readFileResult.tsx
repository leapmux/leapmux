import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ParsedCatLine } from './ReadResultView'
import { createMemo, Show } from 'solid-js'
import { getToolResultExpanded } from '../messageRenderers'
import { toolMessage, toolResultCollapsed, toolResultContentPre } from '../toolStyles.css'
import { ReadResultView } from './ReadResultView'
import { useCollapsedItems } from './useCollapsedLines'

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

// Stable empty fallback so memo equality holds when `lines` is null —
// otherwise every read re-allocates `[]` and downstream `displayItems`
// trips its equality check on every render.
const EMPTY_LINES: readonly ParsedCatLine[] = []

export function ReadFileResultBody(props: {
  source: ReadFileResultSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const items = createMemo<ParsedCatLine[]>(() => props.source.lines ?? (EMPTY_LINES as ParsedCatLine[]))
  const hasParsedLines = () => props.source.lines !== null
  const { isCollapsed, displayItems } = useCollapsedItems<ParsedCatLine>({ items, expanded })
  const collapsedClass = () => hasParsedLines() && isCollapsed() ? ` ${toolResultCollapsed}` : ''

  return (
    <div class={`${toolMessage}${collapsedClass()}`}>
      <Show
        when={hasParsedLines() && items().length > 0}
        fallback={<div class={toolResultContentPre}>{props.source.fallbackContent || 'Empty file'}</div>}
      >
        <ReadResultView lines={displayItems()} filePath={props.source.filePath} />
      </Show>
    </div>
  )
}
