/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import MessageSquare from 'lucide-solid/icons/message-square'
import { isObject } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { useSharedExpandedState } from '../../messageRenderers'
import {
  agentErrorRenderer,
  agentRenamedRenderer,
  apiRetryRenderer,
  compactBoundaryRenderer,
  compactingRenderer,
  contextClearedRenderer,
  controlResponseRenderer,
  interruptedRenderer,
  microcompactBoundaryRenderer,
  settingsChangedRenderer,
  systemInitRenderer,
} from '../../notificationRenderers'
import { taskNotificationRenderer } from '../../taskRenderers'
import {
  COLLAPSED_RESULT_ROWS,
  ToolUseLayout,
} from '../../toolRenderers'
import {
  toolResultCollapsed,
  toolResultContent,
} from '../../toolStyles.css'
import {
  assistantTextRenderer,
  assistantThinkingRenderer,
  planExecutionRenderer,
  tryClaudeUnknownKindRenderers,
  userContentRenderer,
  userTextContentRenderer,
} from './messageRenderers'
import { rateLimitRenderer, resultRenderer } from './notifications'
import { renderClaudeToolResult } from './toolResults'
import { renderClaudeToolUse } from './toolUse'

/** Dispatches rendering for all Claude Code message categories. */
export function renderClaudeMessage(
  category: MessageCategory,
  parsed: unknown,
  role: MessageRole,
  context?: RenderContext,
): JSX.Element | null {
  switch (category.kind) {
    case 'tool_use':
      return renderClaudeToolUse(category, parsed, context)
    case 'tool_result':
      return renderClaudeToolResult(parsed, context)
    case 'agent_prompt':
      return renderClaudeAgentPrompt(parsed, context)
    case 'assistant_text':
      return assistantTextRenderer.render(parsed, role, context)
    case 'assistant_thinking':
      return assistantThinkingRenderer.render(parsed, role, context)
    case 'user_text':
      return userTextContentRenderer.render(parsed, role, context)
    case 'user_content':
      return userContentRenderer.render(parsed, role, context)
    case 'plan_execution':
      return planExecutionRenderer.render(parsed, role, context)
    case 'task_notification':
      return taskNotificationRenderer.render(parsed, role, context)
    case 'notification':
      return claudeNotificationRenderer(parsed, role, context)
    case 'result_divider':
      return resultRenderer.render(parsed, role, context)
    case 'control_response':
      return controlResponseRenderer.render(parsed, role, context)
    case 'compact_summary':
    case 'hidden':
      return <span />
    case 'unknown':
      return tryClaudeUnknownKindRenderers(parsed, role, context)
    default:
      return null
  }
}

/**
 * Walk the Claude-shaped notification renderers in order. Each handles its
 * own type detection on `parsed.type` / `parsed.subtype` and returns null
 * when the message isn't its format.
 */
function claudeNotificationRenderer(
  parsed: unknown,
  role: MessageRole,
  context: RenderContext | undefined,
): JSX.Element | null {
  return settingsChangedRenderer.render(parsed, role, context)
    ?? interruptedRenderer.render(parsed, role, context)
    ?? contextClearedRenderer.render(parsed, role, context)
    ?? compactingRenderer.render(parsed, role, context)
    ?? agentErrorRenderer.render(parsed, role, context)
    ?? agentRenamedRenderer.render(parsed, role, context)
    ?? rateLimitRenderer.render(parsed, role, context)
    ?? apiRetryRenderer.render(parsed, role, context)
    ?? compactBoundaryRenderer.render(parsed, role, context)
    ?? microcompactBoundaryRenderer.render(parsed, role, context)
    ?? systemInitRenderer.render(parsed, role, context)
}

/** Collapsed agent prompt view: MessageSquare icon + "Prompt" title + collapsed markdown body. */
function AgentPromptView(props: {
  text: string
  context?: RenderContext
}): JSX.Element {
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, 'agent-prompt')
  const isCollapsed = () => !expanded() && props.text.split('\n').length > COLLAPSED_RESULT_ROWS

  return (
    <ToolUseLayout
      icon={MessageSquare}
      toolName="Prompt"
      title="Prompt"
      context={props.context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderMarkdown(props.text)} />
    </ToolUseLayout>
  )
}

function renderClaudeAgentPrompt(
  parsed: unknown,
  context?: RenderContext,
): JSX.Element | null {
  if (!isObject(parsed) || parsed.type !== 'user' || typeof parsed.parent_tool_use_id !== 'string')
    return null

  const message = parsed.message as Record<string, unknown> | undefined
  if (!isObject(message))
    return null
  const content = (message as Record<string, unknown>).content
  if (!Array.isArray(content))
    return null

  const text = (content as Array<Record<string, unknown>>)
    .filter(c => isObject(c) && c.type === 'text')
    .map(c => String(c.text || ''))
    .join('\n\n')
  if (!text)
    return null

  return <AgentPromptView text={text} context={context} />
}
