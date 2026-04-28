/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { WebSearchLink } from './webSearchExtract'
import { For, Show } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { extractDomain } from '~/lib/url'
import { getToolResultExpanded } from '../messageRenderers'
import {
  toolMessage,
  toolResultCollapsed,
  toolResultContent,
  toolResultPrompt,
  webSearchLink,
  webSearchLinkDomain,
  webSearchLinkList,
  webSearchLinkTitle,
} from '../toolStyles.css'
import { useCollapsedItems } from './useCollapsedLines'

export interface WebSearchResultsSource {
  links: WebSearchLink[]
  summary: string
  /** Echoed query (Claude tool_use_result.query). */
  query?: string
  /** Claude tool_use_result.durationSeconds (note: seconds, not ms). */
  durationSeconds?: number
}

export function WebSearchResultsBody(props: {
  source: WebSearchResultsSource
  context?: RenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const links = () => props.source.links
  const { isCollapsed, displayItems: displayLinks } = useCollapsedItems<WebSearchLink>({ items: links, expanded })

  return (
    <div class={toolMessage}>
      <Show when={links().length > 0}>
        <div class={toolResultPrompt}>
          {pluralize(links().length, 'result')}
        </div>
        <div class={`${webSearchLinkList}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
          <For each={displayLinks()}>
            {link => (
              <div class={webSearchLink}>
                <span class={webSearchLinkTitle}>
                  <a href={link.url} target="_blank" rel="noopener noreferrer nofollow">{link.title}</a>
                </span>
                <span class={webSearchLinkDomain}>{extractDomain(link.url)}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
      <Show when={expanded() && props.source.summary}>
        <div class={toolResultContent} innerHTML={renderMarkdown(props.source.summary)} />
      </Show>
    </div>
  )
}
