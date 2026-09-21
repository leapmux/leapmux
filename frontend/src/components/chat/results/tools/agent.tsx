import type { JSX } from 'solid-js'
import type { AgentRequest } from '../../model/tools/agent'
import type { ParsedCall, ResolvedCall, ToolKindMeta, ToolKindRenderer, ToolRowView } from './renderer'
import Bot from 'lucide-solid/icons/bot'
import { For, Show } from 'solid-js'
import { MESSAGE_UI_KEY } from '../../messageUiKeys'
import { toolInputText, toolResultPrompt } from '../../toolStyles.css'
import { AgentResultBody, agentRunStatesOutcome } from '../agentResult'
import { CollapsibleContent } from '../CollapsibleContent'
import { ToolMetadata } from '../ToolMetadata'
import { textNeedsCollapse } from '../useCollapsedLines'

/** The subagent title: what it was asked to do, and the type that was asked. */
export function renderAgentTitle(description: string, subagentType?: string): JSX.Element | null {
  // A launch that describes nothing has no title to compose, and a span of empty text
  // would head the row with a blank line. Null lets the caller reach the next step of
  // the precedence `renderer.ts` states.
  if (!description)
    return null
  // If description starts with subagent name, use "SubAgent: rest" format;
  // also suppress the trailing "(SubAgent)" suffix since it's already in the title.
  let titleDesc = description
  let showSuffix = true
  if (subagentType) {
    const prefix = subagentType.toLowerCase()
    const descLower = description.toLowerCase()
    if (descLower.startsWith(`${prefix} `)) {
      titleDesc = `${subagentType}: ${description.slice(subagentType.length + 1)}`
      showSuffix = false
    }
  }

  const title = `${titleDesc}${showSuffix && subagentType ? ` (${subagentType})` : ''}`
  // `toolInputText`, because `ToolUseLayout` wraps a STRING title in it and leaves a
  // JSX title alone. An unclassed span lost the monospace face and the one-line clip,
  // so a long description wrapped the header onto extra rows.
  return <span class={toolInputText}>{title}</span>
}

/** The prompt half of an agent launch, drawn while the subagent runs. */
function AgentPromptBody(props: { request: AgentRequest, view: ToolRowView, hasResult: boolean }): JSX.Element {
  const promptLabel = () => props.request.promptLabel || 'Prompt'
  const hasPrompt = () => props.request.prompt.trim() !== ''
  const longPrompt = () => textNeedsCollapse(props.request.prompt)
  const expanded = (): boolean => props.view.expanded()
  return (
    <>
      <Show when={!props.hasResult || expanded()}><ToolMetadata items={props.request.metadata} /></Show>
      <Show when={hasPrompt() && (!props.hasResult || expanded())}>
        <div class={toolResultPrompt}>{promptLabel()}</div>
        <CollapsibleContent kind={props.request.promptFormat === 'pre' ? 'pre' : 'markdown-tool-result'} text={props.request.prompt} isCollapsed={!expanded() && longPrompt()} {...(props.view.context !== undefined ? { context: props.view.context } : {})} />
      </Show>
    </>
  )
}

export const agentRenderer: ToolKindRenderer<'agent'> = {
  icon: Bot,
  label: 'Agent',
  // EVERY agent must state how it ended, not merely exist. A card whose run is still
  // `running` -- or whose state never arrived, which Codex reports as `status
  // unavailable` -- draws the neutral glyph and the child's own word, and says nothing
  // about how the CALL ended. One such card among several leaves the row incomplete,
  // so the shared header states the call's outcome for it.
  statesOwnOutcome: call => call.result.agents.length > 0 && call.result.agents.every(agentRunStatesOutcome),
  requestExpandUiKey: MESSAGE_UI_KEY.AGENT_PROMPT,
  title(call, context) {
    // The registry title wins when the launch created one task: it says what the
    // subagent does, where `description` may hold the tool's own name alone.
    const registryTitle = call.request.registryKey
      ? context?.subagents?.row(call.request.registryKey)?.title?.trim()
      : undefined
    // The REQUEST, then the call's own title, then the last resort -- the precedence
    // `renderer.ts` states. A `|| 'Task'` spelled INSIDE the first step made the
    // second one dead, so a launch that described nothing drew that word rather than
    // the words the provider's own frame carried.
    //
    // The last resort is `Task`, not the kind's `Agent` label: it is the launch word
    // every provider shares, and `messageRenderers.test.tsx` pins it. The label heads
    // the icon's tooltip instead.
    return renderAgentTitle(registryTitle || call.request.description.trim(), call.request.agentType?.trim())
      ?? call.title
      ?? 'Task'
  },
  request(call, view) {
    return (
      <Show when={view.role !== 'result'}>
        <AgentPromptBody request={call.request} view={view} hasResult={view.hasResultRow ?? false} />
      </Show>
    )
  },
  result(call: ResolvedCall<'agent'>, view) {
    return <For each={call.result.agents}>{agent => <AgentResultBody source={agent} {...(view.context !== undefined ? { context: view.context } : {})} />}</For>
  },
  requestMeta(call: ParsedCall<'agent'>, hasResult: boolean): Partial<ToolKindMeta> {
    const hasPrompt = call.request.prompt.trim() !== ''
    const promptLabel = (call.request.promptLabel || 'Prompt').toLowerCase()
    const hasPromptOrMeta = hasPrompt || (call.request.metadata?.length ?? 0) > 0
    const longPrompt = textNeedsCollapse(call.request.prompt)
    return {
      collapsible: hasPromptOrMeta && (hasResult || longPrompt),
      // A launch with no prompt still offers its metadata, and "details" is the
      // word for that: "Show prompt" would promise words it does not carry.
      expandLabel: hasPrompt ? `Show ${promptLabel}` : 'Show details',
      copyableContent: () => call.request.prompt || null,
      ...(hasPrompt ? { copyLabel: `Copy ${promptLabel}` } : {}),
    }
  },
  resultMeta(call: ResolvedCall<'agent'>): ToolKindMeta {
    return {
      collapsible: call.result.agents.some(agent => textNeedsCollapse(agent.body)),
      hasDiff: false,
      copyableContent: () => call.result.agents.map(agent => agent.body).filter(Boolean).join('\n\n') || null,
    }
  },
}
