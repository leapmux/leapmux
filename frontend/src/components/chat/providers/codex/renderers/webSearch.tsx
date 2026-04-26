import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Globe from 'lucide-solid/icons/globe'
import { For, Show } from 'solid-js'
import { useSharedExpandedState } from '../../../messageRenderers'
import { WebSearchActionBody, webSearchActionDetail } from '../../../results/webSearchResult'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'
import { renderQueryTitle } from '../../../toolTitleRenderers'
import { codexWebSearchActionFromItem } from '../extractors/webSearch'
import { extractItem } from '../renderHelpers'

/** Renders Codex webSearch items using WebSearch/WebFetch-style tool cards. */
export function codexWebSearchRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'webSearch')
    return null

  const action = codexWebSearchActionFromItem(item)
  if (!action)
    return null

  const queries = action.type === 'search' ? action.queries : []
  const detail = webSearchActionDetail(action)
  const isStartMessage = action.type === 'other' && !action.query.trim()
  const [expanded, setExpanded] = useSharedExpandedState(() => context, 'codex-web-search')

  if (isStartMessage) {
    return (
      <ToolUseLayout
        icon={Globe}
        toolName="WebSearch"
        title="Searching the web"
        alwaysVisible={true}
        context={context}
      />
    )
  }

  if (!detail.trim())
    return null

  return (
    <ToolUseLayout
      icon={Globe}
      toolName={action.type === 'openPage' ? 'WebFetch' : 'WebSearch'}
      title={<WebSearchActionBody source={{ action }} context={context} />}
      context={context}
      expanded={expanded()}
      onToggleExpand={queries.length > 1 ? () => setExpanded(v => !v) : undefined}
      alwaysVisible={queries.length <= 1}
    >
      <Show when={queries.length > 1}>
        <For each={queries.slice(1)}>
          {extraQuery => (
            <div class={toolInputSummary}>
              {renderQueryTitle(extraQuery) || extraQuery}
            </div>
          )}
        </For>
      </Show>
    </ToolUseLayout>
  )
}
