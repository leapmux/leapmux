/* eslint-disable solid/no-innerhtml -- HTML is produced via renderMarkdown, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { COLLAPSED_RESULT_ROWS } from '../../../toolRenderers'
import { toolResultCollapsed, toolResultContent } from '../../../toolStyles.css'

function formatAgentStatus(status: string): string {
  if (status === 'async_launched')
    return 'launched asynchronously'
  return status
}

/** Collapsed Agent result view: icon + "Agent {agentId} {status}" header + collapsed markdown body. */
export function AgentResultView(props: {
  agentId: string
  status: string
  content: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded?.() ?? false
  const isCollapsed = () => !expanded() && props.content.split('\n').length > COLLAPSED_RESULT_ROWS
  const icon = () => props.status === 'completed' ? Check : Bot

  return (
    <ToolStatusHeader icon={icon()} title={`Agent ${props.agentId} ${formatAgentStatus(props.status)}`}>
      <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.content)} />
    </ToolStatusHeader>
  )
}
