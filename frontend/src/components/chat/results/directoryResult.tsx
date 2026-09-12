import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { FileListEntry } from './searchResult'
import { Show } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { getToolResultExpanded } from '../messageRenderers'
import { toolMessage, toolResultCollapsed, toolResultPrompt } from '../toolStyles.css'
import { COLLAPSED_RESULT_ROWS } from './collapse'
import { FileListView } from './searchResult'
import { useCollapsedItems } from './useCollapsedLines'

export interface DirectoryResultSource {
  entries: FileListEntry[]
  totalEntries?: number
  offset?: number
  truncated?: boolean
  notice?: string
}

export function directoryResultCollapsible(source: DirectoryResultSource): boolean {
  return source.entries.length > COLLAPSED_RESULT_ROWS
}

/** Directory tools share entry counts, pagination, and file-list formatting. */
export function DirectoryResultBody(props: { source: DirectoryResultSource, context?: RenderContext }): JSX.Element {
  const entries = useCollapsedItems({ items: () => props.source.entries, expanded: () => getToolResultExpanded(props.context) })
  const summary = () => {
    const count = props.source.entries.length
    const total = props.source.totalEntries
    const offset = props.source.offset ?? 1
    if (count === 0)
      return (total ?? 0) > 0 || props.source.truncated ? 'No entries shown' : 'Empty directory'
    if (total !== undefined && total >= count && (offset > 1 || total > count))
      return `Entries ${offset}–${offset + count - 1} of ${total}`
    return pluralize(count, 'entry', 'entries')
  }
  return (
    <div class={`${toolMessage}${entries.isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <div class={toolResultPrompt}>{summary()}</div>
      <FileListView entries={entries.displayItems()} context={props.context} />
      <Show when={props.source.notice || props.source.truncated}>
        <div class={toolResultPrompt}>{props.source.notice || 'Output truncated'}</div>
      </Show>
    </div>
  )
}
