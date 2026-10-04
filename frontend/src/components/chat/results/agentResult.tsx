import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { AgentRun, AgentRunStatus } from '../model/tools/agent'
import type { ToolResultRenderContext } from '../renderContext'
import Bot from 'lucide-solid/icons/bot'
import { Show } from 'solid-js'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { getToolResultExpanded } from '../messageRenderers'
import { agentRunStatusLabel } from '../model/tools/agent'
import { toolResultPrompt } from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { ENDED_OUTCOME_ICON } from './endedOutcomeIcon'
import { ToolMetadata } from './ToolMetadata'
import { ToolStatusHeader } from './ToolStatusHeader'
import { useCollapsedFlag } from './useCollapsedLines'

/** Limit the description so the outcome remains visible in the status header. */
function agentResultTitle(source: AgentRun, context?: ToolResultRenderContext): string {
  const description = clipFirstLine(source.description || (source.registryKey ? context?.subagents?.row(source.registryKey)?.title ?? '' : ''), 80)
  const identity = description ? `"${description}"` : source.agentId
  return ['Agent', identity, agentRunStatusLabel(source)].filter(Boolean).join(' ')
}

/**
 * Icons for outcomes that ended the agent run.
 * An absent icon means the run did not reach an ended outcome.
 * The card then describes its current state.
 * agentRunStatesOutcome reads this same table to keep the shared header consistent.
 */
const AGENT_OUTCOME_ICON: Partial<Record<AgentRunStatus, LucideIcon>> = ENDED_OUTCOME_ICON

/**
 * Report whether this agent card states an ended outcome.
 * The shared row header remains when the child is still running or its status is unavailable.
 * A failed call must still show its own failure when the child state supplies no ended outcome.
 */
export function agentRunStatesOutcome(run: AgentRun): boolean {
  return AGENT_OUTCOME_ICON[run.outcome] !== undefined
}

/** Render an agent's status, identifying fields, and formatted report or launch prompt. */
export function AgentResultBody(props: { source: AgentRun, context?: ToolResultRenderContext }): JSX.Element {
  const collapsed = useCollapsedFlag({ text: () => props.source.body, expanded: () => getToolResultExpanded(props.context) })
  const icon = () => AGENT_OUTCOME_ICON[props.source.outcome] ?? Bot
  return (
    <ToolStatusHeader icon={icon()} title={agentResultTitle(props.source, props.context)}>
      <ToolMetadata items={props.source.metadata} />
      <Show when={props.source.body}>
        <Show when={props.source.bodyLabel}><div class={toolResultPrompt}>{props.source.bodyLabel}</div></Show>
        <CollapsibleContent outputPreview kind="markdown-tool-result" text={props.source.body} isCollapsed={collapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </ToolStatusHeader>
  )
}
