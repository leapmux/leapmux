import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import { getToolResultExpanded } from '../../../messageRenderers'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { useCollapsedFlag } from '../../../results/useCollapsedLines'

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
  const isCollapsed = useCollapsedFlag({
    text: () => props.content,
    expanded: () => getToolResultExpanded(props.context),
  })
  const icon = () => props.status === 'completed' ? Check : Bot

  return (
    <ToolStatusHeader icon={icon()} title={`Agent ${props.agentId} ${formatAgentStatus(props.status)}`}>
      <CollapsibleContent kind="markdown-tool-result" text={props.content} isCollapsed={isCollapsed()} />
    </ToolStatusHeader>
  )
}
