import type { JSX } from 'solid-js'
import type { ListResult } from '../ir/tools/list'
import type { RenderContext } from '../messageRenderers'
import { Show } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { getToolResultExpanded } from '../messageRenderers'
import { toolMessage, toolResultCollapsed, toolResultPrompt } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { FileListView } from './searchResult'
import { useCollapsedItems } from './useCollapsedLines'

/** The body of one `list` call: the entry count, the page range, and the file list. */
export function ListResultBody(props: { source: ListResult, context?: RenderContext }): JSX.Element {
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
      <FileListView entries={entries.displayItems()} {...(props.context !== undefined ? { context: props.context } : {})} />
      <Show when={props.source.notice || props.source.truncated}>
        <div class={toolResultPrompt}>{props.source.notice || TRUNCATION_NOTICE}</div>
      </Show>
    </div>
  )
}
