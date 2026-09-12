import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import OctagonX from 'lucide-solid/icons/octagon-x'
import { Show } from 'solid-js'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { getToolResultExpanded } from '../messageRenderers'
import { toolResultPrompt } from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { ToolMetadata } from './ToolMetadata'
import { ToolStatusHeader } from './ToolStatusHeader'
import { useCollapsedFlag } from './useCollapsedLines'

/** Each provider resolves its native state and fields before this component renders them. */
export interface AgentResultSource {
  description: string
  registryKey?: string
  agentId: string
  status: string
  outcome: 'completed' | 'failed' | 'running' | 'stopped' | 'unknown'
  metadata: Array<{ label: string, value: string }>
  body: string
  bodyLabel?: string
}

/** Limit the description so the outcome remains visible in the status header. */
function agentResultTitle(source: AgentResultSource, context?: RenderContext): string {
  const description = clipFirstLine(source.description || (source.registryKey ? context?.sources?.backgroundTask(source.registryKey)?.title ?? '' : ''), 80)
  const identity = description ? `"${description}"` : source.agentId
  return ['Agent', identity, source.status].filter(Boolean).join(' ')
}

/** Render an agent's status, identifying fields, and formatted report or launch prompt. */
export function AgentResultBody(props: { source: AgentResultSource, context?: RenderContext }): JSX.Element {
  const collapsed = useCollapsedFlag({ text: () => props.source.body, expanded: () => getToolResultExpanded(props.context) })
  const icon = () => props.source.outcome === 'completed' ? Check : props.source.outcome === 'failed' ? CircleAlert : props.source.outcome === 'stopped' ? OctagonX : Bot
  return (
    <ToolStatusHeader icon={icon()} title={agentResultTitle(props.source, props.context)}>
      <ToolMetadata items={props.source.metadata} />
      <Show when={props.source.body}>
        <Show when={props.source.bodyLabel}><div class={toolResultPrompt}>{props.source.bodyLabel}</div></Show>
        <CollapsibleContent kind="markdown-tool-result" text={props.source.body} isCollapsed={collapsed()} context={props.context} />
      </Show>
    </ToolStatusHeader>
  )
}
