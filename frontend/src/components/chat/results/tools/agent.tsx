import type { JSX } from 'solid-js'
import type { AgentRequest } from '../../model/tools/agent'
import type { RequestSideCall, ResolvedCall, ToolKindMeta, ToolKindRenderer, ToolRowView } from './renderer'
import Bot from 'lucide-solid/icons/bot'
import { For, Show } from 'solid-js'
import { MarkdownPrefetch } from '../../MarkdownText'
import { MESSAGE_UI_KEY } from '../../messageUiKeys'
import { toolInputText, toolResultPrompt } from '../../toolStyles.css'
import { AgentResultBody, agentRunStatesOutcome } from '../agentResult'
import { CollapsibleContent } from '../CollapsibleContent'
import { ENDED_OUTCOME_ICON } from '../endedOutcomeIcon'
import { ToolMetadata } from '../ToolMetadata'
import { ToolStatusHeader } from '../ToolStatusHeader'
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

  const title = `${titleDesc}${showSuffix && subagentType
    ? ` (${subagentType})`
    : ''}`
  // `toolInputText`, because `ToolUseLayout` wraps a STRING title in it and leaves a
  // JSX title alone. An unclassed span lost the monospace face and the one-line clip,
  // so a long description wrapped the header onto extra rows.
  return <span class={toolInputText}>{title}</span>
}

/** The registry can supply the description independently of the message revision. */
export function agentRegistryTitle(request: AgentRequest, titleOf: (key: string) => string | undefined): string | undefined {
  return request.registryKey
    ? titleOf(request.registryKey)?.trim() || undefined
    : undefined
}

/**
 * The prompt half of an agent launch: its metadata and the prompt it sent.
 *
 * `clip` clips a long prompt to its first lines, as the pending row draws it while the
 * subagent runs. The request row draws it whole, when the reader expands the row.
 */
function AgentPromptBody(props: { request: AgentRequest, view: ToolRowView, clip: boolean }): JSX.Element {
  const promptLabel = () => props.request.promptLabel || 'Prompt'
  return (
    <>
      <ToolMetadata items={props.request.metadata} />
      <Show when={agentHasPrompt(props.request)}>
        <div class={toolResultPrompt}>{promptLabel()}</div>
        <CollapsibleContent
          kind={agentPromptIsMarkdown(props.request)
            ? 'markdown-tool-result'
            : 'pre'}
          text={props.request.prompt}
          isCollapsed={props.clip && textNeedsCollapse(props.request.prompt)}
          {...(props.view.context !== undefined
            ? { context: props.view.context }
            : {})}
        />
      </Show>
    </>
  )
}

/** Whether an agent launch states a prompt to draw. */
function agentHasPrompt(request: AgentRequest): boolean {
  return request.prompt.trim() !== ''
}

/** Whether an agent launch's prompt reads as Markdown, rather than as preformatted text. */
function agentPromptIsMarkdown(request: AgentRequest): boolean {
  return request.promptFormat !== 'pre'
}

/** Whether an agent launch states a prompt or metadata to draw. */
function hasPromptOrMetadata(request: AgentRequest): boolean {
  return agentHasPrompt(request) || (request.metadata?.length ?? 0) > 0
}

/** Whether a successful agent result needs one shared status instead of per-agent cards. */
function isEmptyCompletedAgentResult(call: ResolvedCall<'agent'>): boolean {
  return call.status === 'completed' && call.result.agents.length === 0
}

export const agentRenderer: ToolKindRenderer<'agent'> = {
  icon: Bot,
  label: 'Agent',
  // EVERY agent must state how it ended, not merely exist. A card whose run is still
  // `running` -- or whose state never arrived, which Codex reports as `status
  // unavailable` -- draws the neutral glyph and the child's own word, and says nothing
  // about how the CALL ended. One such card among several leaves the row incomplete,
  // so the shared header states the call's outcome for it. A completed empty result
  // draws its own status below because no agent card can state the outcome.
  statesOwnOutcome: call => isEmptyCompletedAgentResult(call)
    || (call.result.agents.length > 0 && call.result.agents.every(agentRunStatesOutcome)),
  requestExpandUiKey: MESSAGE_UI_KEY.AGENT_PROMPT,
  title(call, context) {
    // The registry title wins when the launch created one task: it says what the
    // subagent does, where `description` may hold the tool's own name alone.
    const registryTitle = agentRegistryTitle(call.request, key => context?.subagents?.row(key)?.title)
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
  // The request row draws the prompt only when the reader expands it. It draws the
  // same whether or not the subagent still runs, because it never reads its result.
  // While the subagent runs, the pending row below it shows the prompt instead.
  request(call, view) {
    return (
      <Show when={view.role !== 'result'}>
        <Show
          when={view.expanded()}
          fallback={agentHasPrompt(call.request) && agentPromptIsMarkdown(call.request)
            ? <MarkdownPrefetch text={call.request.prompt} context={view.context} />
            : null}
        >
          <AgentPromptBody request={call.request} view={view} clip={false} />
        </Show>
      </Show>
    )
  },
  // While the subagent runs, the pending row states what it was asked. An expanded
  // request row already draws the whole prompt, so the pending row then draws none.
  pending(call, view) {
    return (
      <Show when={!view.expanded() && hasPromptOrMetadata(call.request)}>
        <AgentPromptBody request={call.request} view={view} clip />
      </Show>
    )
  },
  // The row that ended a call with no result restates no prompt: the request row of the
  // call offers the whole prompt on Expand, in every state of the call.
  withoutTypedResult: () => null,
  result(call: ResolvedCall<'agent'>, view) {
    return (
      <Show
        when={call.result.agents.length > 0}
        fallback={isEmptyCompletedAgentResult(call)
          ? <ToolStatusHeader icon={ENDED_OUTCOME_ICON.completed} title="Completed" />
          : null}
      >
        <For each={call.result.agents}>
          {agent => (
            <AgentResultBody
              source={agent}
              {...(view.context !== undefined
                ? { context: view.context }
                : {})}
            />
          )}
        </For>
      </Show>
    )
  },
  requestMeta(call: RequestSideCall<'agent'>): Partial<ToolKindMeta> {
    const hasPrompt = agentHasPrompt(call.request)
    const promptLabel = (call.request.promptLabel || 'Prompt').toLowerCase()
    return {
      // The request row draws the prompt only on Expand, so a launch that states one
      // always offers it.
      collapsible: hasPromptOrMetadata(call.request),
      // A launch with no prompt still offers its metadata, and "details" is the
      // word for that: "Show prompt" would promise words it does not carry.
      expandLabel: hasPrompt
        ? `Show ${promptLabel}`
        : 'Show details',
      copyableContent: () => call.request.prompt || null,
      ...(hasPrompt
        ? { copyLabel: `Copy ${promptLabel}` }
        : {}),
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
