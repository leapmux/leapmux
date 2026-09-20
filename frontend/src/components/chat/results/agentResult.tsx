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
 * The glyph each ENDED outcome takes.
 *
 * Partial on purpose: an outcome absent from it is one the run has not reached, so
 * this card states what the subagent is rather than how it finished.
 * {@link agentRunStatesOutcome} reads the same table, so the glyph and the answer the
 * row's header depends on cannot drift.
 */
const AGENT_OUTCOME_ICON: Partial<Record<AgentRunStatus, LucideIcon>> = ENDED_OUTCOME_ICON

/**
 * Whether ONE run's card states how that run ended.
 *
 * `agentRenderer` asks it of every run before it suppresses the shared outcome header,
 * because a card that shows the neutral glyph and the word `running` says nothing about
 * how the CALL ended. A launch that failed while its child state still read `running`
 * -- or read nothing, which Codex reports as `status unavailable` -- drew a row whose
 * every line was about the child and no line about the failure.
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
        <CollapsibleContent kind="markdown-tool-result" text={props.source.body} isCollapsed={collapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </ToolStatusHeader>
  )
}
