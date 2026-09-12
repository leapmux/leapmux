import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { createMemo, For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { getToolResultExpanded } from '../messageRenderers'
import {
  toolMessage,
  toolResultCollapsed,
  toolResultContentPre,
  toolResultPrompt,
} from '../toolStyles.css'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { useCollapsedItems, useCollapsedLines } from './useCollapsedLines'

export type SearchVariant = 'grep' | 'glob' | 'search'

export interface SearchResultLine {
  filePath: string
  lineNumber?: number
  text: string
}

/**
 * Provider-neutral source for Grep/Glob/ACP-search results. The body branches
 * on `variant` for the summary phrasing; the file list and content blob
 * rendering are shared.
 */
export interface SearchResultSource {
  variant: SearchVariant
  pattern?: string
  filenames: string[]
  /** Grep-style content blob (line:text or file:line:text). Empty otherwise. */
  content: string
  /** Structured matches keep file paths separate from text that can contain path punctuation. */
  lines?: SearchResultLine[]
  numFiles: number
  numLines: number
  /** Grep count-mode: tool_use_result.numMatches. */
  numMatches?: number
  /** ACP search: rawOutput.metadata.matches. */
  matches?: number
  /**
   * Result truncated by the tool's own cap (Glob explicit `truncated`,
   * Grep `appliedLimit != null`).
   */
  truncated: boolean
  /** A provider's explanation of an output limit. */
  notice?: string
  /** Glob: tool_use_result.durationMs. */
  durationMs?: number
  /** Grep: output_mode — 'content' / 'files_with_matches' / 'count'. */
  mode?: string
  /** Raw fallback text shown when there's no structured output. */
  fallbackContent: string
}

/** Body and toolbar consumers use the same normalized search text. */
export function searchResultText(source: SearchResultSource, context?: RenderContext): string {
  if (source.lines?.length)
    return source.lines.map(line => `${relativizePath(line.filePath, context?.workingDir, context?.homeDir)}${line.lineNumber !== undefined ? `:${line.lineNumber}` : ''}:${line.text}`).join('\n')
  return source.content || (source.filenames.length === 0 ? source.fallbackContent : '')
}

export function searchResultCollapsible(source: SearchResultSource): boolean {
  return source.filenames.length > COLLAPSED_RESULT_ROWS || hasMoreLinesThan(searchResultText(source), COLLAPSED_RESULT_ROWS)
}

export interface FileListEntry {
  path: string
  detail?: string
}

/** Reusable file paths with optional provider-supplied details. */
export function FileListView(props: {
  entries: FileListEntry[]
  context?: RenderContext
}): JSX.Element {
  return (
    <div class={toolResultContentPre}>
      <For each={props.entries}>
        {(f, i) => (
          <>
            {i() > 0 && '\n'}
            {relativizePath(f.path, props.context?.workingDir, props.context?.homeDir)}
            <Show when={f.detail}>{detail => `\t${detail()}`}</Show>
          </>
        )}
      </For>
    </div>
  )
}

function emptySummaryFor(source: SearchResultSource, marker: string): string {
  // Empty result: route to a muted prompt summary only when we have positive
  // evidence (no fallback text or the canonical marker for this variant).
  // Unknown fallback text falls through to the pre-content body.
  const fc = source.fallbackContent.trim()
  if (!fc || fc === marker)
    return marker
  return ''
}

function summaryFor(source: SearchResultSource): string {
  if (source.variant === 'grep') {
    // Count mode: total occurrences across N files, even when zero.
    if (source.mode === 'count' && typeof source.numMatches === 'number')
      return `${pluralize(source.numMatches, 'match', 'matches')} in ${pluralize(source.numFiles, 'file')}`
    if (source.numLines > 0 && source.numFiles > 0)
      return `${pluralize(source.numLines, 'match', 'matches')} in ${pluralize(source.numFiles, 'file')}`
    if (source.numFiles > 0)
      return `Found ${pluralize(source.numFiles, 'file')}`
    return emptySummaryFor(source, 'No matches found')
  }
  if (source.variant === 'glob') {
    if (source.numFiles > 0)
      return `Found ${pluralize(source.numFiles, 'file')}`
    return emptySummaryFor(source, 'No files found')
  }
  // ACP search
  if (typeof source.matches === 'number' && source.matches >= 0) {
    if (source.matches === 0)
      return 'No matches found'
    return `Found ${pluralize(source.matches, 'match', 'matches')}`
  }
  return ''
}

export function SearchResultBody(props: {
  source: SearchResultSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const filenames = () => props.source.filenames
  const summary = createMemo(() => summaryFor(props.source))
  const content = createMemo(() => {
    const text = searchResultText(props.source, props.context)
    return text.trim() === summary() ? '' : text
  })
  const filenameCollapse = useCollapsedItems<string>({ items: filenames, expanded })
  const contentCollapse = useCollapsedLines({ text: content, expanded })
  const isCollapsed = () => filenameCollapse.isCollapsed() || contentCollapse.isCollapsed()
  const displayFilenames = filenameCollapse.displayItems
  const displayContent = contentCollapse.display

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show when={summary()}>
        <div class={toolResultPrompt}>{summary()}</div>
      </Show>
      <Show when={displayFilenames().length > 0}>
        <FileListView entries={displayFilenames().map(path => ({ path }))} context={props.context} />
      </Show>
      <Show when={displayContent()}>
        <div class={toolResultContentPre}>{displayContent()}</div>
      </Show>
      <Show when={props.source.notice || props.source.truncated}>
        <div class={toolResultPrompt}>{props.source.notice || 'Output truncated'}</div>
      </Show>
    </div>
  )
}
