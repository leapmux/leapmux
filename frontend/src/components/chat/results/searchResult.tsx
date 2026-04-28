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
import { useCollapsedItems, useCollapsedLines } from './useCollapsedLines'

export type SearchVariant = 'grep' | 'glob' | 'search'

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
  /** Glob: tool_use_result.durationMs. */
  durationMs?: number
  /** Grep: output_mode — 'content' / 'files_with_matches' / 'count'. */
  mode?: string
  /** Raw fallback text shown when there's no structured output. */
  fallbackContent: string
}

/** Reusable file-path list. */
export function FileListView(props: {
  filenames: string[]
  context?: RenderContext
}): JSX.Element {
  return (
    <div class={toolResultContentPre}>
      <For each={props.filenames}>
        {(f, i) => (
          <>
            {i() > 0 && '\n'}
            {relativizePath(f, props.context?.workingDir, props.context?.homeDir)}
          </>
        )}
      </For>
    </div>
  )
}

function summaryFor(source: SearchResultSource): string {
  if (source.variant === 'grep') {
    if (source.numLines > 0 && source.numFiles > 0)
      return `${pluralize(source.numLines, 'match', 'matches')} in ${pluralize(source.numFiles, 'file')}`
    if (source.numFiles > 0)
      return `Found ${pluralize(source.numFiles, 'file')}`
    return ''
  }
  if (source.variant === 'glob') {
    if (source.numFiles > 0)
      return `Found ${pluralize(source.numFiles, 'file')}`
    return ''
  }
  // ACP search
  if (typeof source.matches === 'number' && source.matches >= 0) {
    if (source.matches === 0)
      return 'No matches found'
    return `Found ${pluralize(source.matches, 'match', 'matches')}`
  }
  return ''
}

function emptyFallback(source: SearchResultSource): string {
  if (source.variant === 'grep')
    return source.fallbackContent || 'No matches found'
  if (source.variant === 'glob')
    return source.fallbackContent || 'No files found'
  return source.fallbackContent || ''
}

export function SearchResultBody(props: {
  source: SearchResultSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const filenames = () => props.source.filenames
  const content = () => props.source.content
  const summary = createMemo(() => summaryFor(props.source))
  const filenameCollapse = useCollapsedItems<string>({ items: filenames, expanded })
  const contentCollapse = useCollapsedLines({ text: content, expanded })
  const isCollapsed = () => filenameCollapse.isCollapsed() || contentCollapse.isCollapsed()
  const displayFilenames = filenameCollapse.displayItems
  const displayContent = contentCollapse.display

  const hasResult = () => {
    if (props.source.variant === 'grep')
      return props.source.numFiles > 0 || props.source.numLines > 0
    if (props.source.variant === 'glob')
      return filenames().length > 0
    return typeof props.source.matches === 'number' && props.source.matches > 0
  }

  const fallbackEl = () => {
    if (props.source.variant === 'search' && summary())
      return <div class={toolResultPrompt}>{summary()}</div>
    return <div class={toolResultContentPre}>{emptyFallback(props.source)}</div>
  }

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show when={hasResult()} fallback={fallbackEl()}>
        <Show when={summary()}>
          <div class={toolResultPrompt}>{summary()}</div>
        </Show>
        <Show when={displayFilenames().length > 0}>
          <FileListView filenames={displayFilenames()} context={props.context} />
        </Show>
        <Show when={displayContent()}>
          <div class={toolResultContentPre}>{displayContent()}</div>
        </Show>
      </Show>
    </div>
  )
}
