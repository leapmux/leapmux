import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ParsedCatLine, ReadReminder } from './ReadResultView'
import { createMemo, For, Show } from 'solid-js'
import { Alert } from '~/components/common/Alert'
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
  /** Pre-parsed cat-n lines, synthesized file lines, or null when unparseable / non-text. */
  lines: ParsedCatLine[] | null
  /** Total file lines (Claude tool_use_result.file.totalLines). 0 when unknown. */
  totalLines: number
  /** Returned lines count from Claude tool_use_result.file. 0 when unknown. */
  numLines: number
  /** Raw fallback content used when `lines` is null. */
  fallbackContent: string
  /** `<tag>...</tag>` blocks before the body (e.g. a partial-view notice), shown as alerts when expanded. */
  leading?: ReadReminder[]
  /** `<tag>...</tag>` blocks after the body (e.g. usage reminders), shown as alerts when expanded. */
  trailing?: ReadReminder[]
}

/**
 * Build a shared ReadFileResultSource from raw file content plus a starting
 * line number. Claude's structured Read payloads and Pi's plain-text Read
 * results both carry real file content rather than cat-n output; normalizing
 * them here lets both providers use the same line-numbered/highlighted body.
 */
export function readFileSourceFromContent(args: {
  filePath: string
  content: string
  startLine?: number
  totalLines?: number
  numLines?: number
  fallbackContent?: string
}): ReadFileResultSource {
  const startLine = args.startLine ?? 1
  const lines = args.content
    ? args.content.split('\n').map((text, i) => ({ num: startLine + i, text }))
    : []
  return {
    filePath: args.filePath,
    lines,
    totalLines: args.totalLines ?? 0,
    numLines: args.numLines ?? 0,
    fallbackContent: args.fallbackContent ?? args.content,
  }
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
      {/* Reminder/tag alerts render only when expanded, so the collapsed default
          stays the body-only height the off-screen estimator assumes. */}
      <Show when={expanded()}>
        <For each={props.source.leading ?? []}>
          {r => <Alert variant={r.variant} label={r.label}>{r.text}</Alert>}
        </For>
      </Show>
      <Show
        when={hasParsedLines() && items().length > 0}
        fallback={<div class={toolResultContentPre}>{props.source.fallbackContent || 'Empty file'}</div>}
      >
        <ReadResultView lines={displayItems()} filePath={props.source.filePath} />
      </Show>
      <Show when={expanded()}>
        <For each={props.source.trailing ?? []}>
          {r => <Alert variant={r.variant} label={r.label}>{r.text}</Alert>}
        </For>
      </Show>
    </div>
  )
}
