import type { JSX } from 'solid-js'
import type { ToolKindRenderer } from './renderer'
import Globe from 'lucide-solid/icons/globe'
import { Show } from 'solid-js'
import { toolInputCode, toolInputText } from '../../toolStyles.css'
import { COLLAPSED_RESULT_ROWS } from '../collapse'
import { textNeedsCollapse } from '../useCollapsedLines'
import { WebSearchResultsBody } from '../webSearchResults'
import { renderQueryTitle, renderUrlTitle } from './titleParts'

/** The summary a web search stated, or its links written out one per pair of lines. */
function searchLinksText(result: { summary: string, links: Array<{ title: string, url: string }> }): string {
  return result.summary || result.links.map(link => `${link.title}\n${link.url}`).join('\n\n')
}

/**
 * What a web-search row says it did: the query, or the find-in-page it ran.
 *
 * The pattern takes `toolInputCode` and the joining words take `toolInputText`,
 * exactly as `renderSearchTitle` gives its own pattern one. `ToolUseLayout` wraps a
 * STRING title in `toolInputText` and leaves a JSX title alone, so unclassed spans
 * reached the header with no monospace face and no one-line clip -- and a long
 * pattern wrapped the header onto extra rows.
 */
function webSearchTitle(request: { query: string, queries?: string[], inPage?: { pattern: string, url?: string } }): JSX.Element | null {
  if (request.inPage) {
    const { pattern, url } = request.inPage
    if (url) {
      return (
        <>
          <span class={toolInputCode}>{`"${pattern}"`}</span>
          <span class={toolInputText}>{' in '}</span>
          {renderUrlTitle(url)}
        </>
      )
    }
    return <span class={toolInputCode}>{`"${pattern}"`}</span>
  }
  return renderQueryTitle(request.query)
}

export const webSearchRenderer: ToolKindRenderer<'web_search'> = {
  icon: Globe,
  label: 'Web Search',
  title(call) {
    return webSearchTitle(call.request) ?? call.title ?? 'Web Search'
  },
  // A row that has not drawn its result still states its queries: one search
  // runs several, and the row is the only place a reader can see them all.
  request(call, view) {
    return (
      <Show when={call.result === undefined}>
        <WebSearchResultsBody source={{ links: [], summary: '' }} request={call.request} {...(view.context !== undefined ? { context: view.context } : {})} />
      </Show>
    )
  },
  result(call, view) {
    return <WebSearchResultsBody source={call.result} request={call.request} {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  requestMeta(call) {
    // One search often runs several queries; the paired request row holds the
    // header for the pair, so its toolbar offers the expand that reveals them.
    return {
      collapsible: (call.request.queries?.length ?? 0) > 1,
      copyableContent: () => call.request.query || null,
    }
  },
  resultMeta(call) {
    return {
      collapsible: call.result.links.length > COLLAPSED_RESULT_ROWS
        || (call.request.queries?.length ?? 0) > 1
        || textNeedsCollapse(call.result.summary),
      hasDiff: false,
      // Built INSIDE the closure. Hoisted, it joined every link's title and address on
      // every `toolCallMeta` call to answer whether a Copy button belongs on the row.
      copyableContent: () => searchLinksText(call.result) || null,
    }
  },
}
