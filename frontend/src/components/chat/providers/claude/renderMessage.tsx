/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import MessageSquare from 'lucide-solid/icons/message-square'
import { createMemo } from 'solid-js'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { useSharedExpandedState } from '../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../messageUiKeys'
import {
  agentErrorRenderer,
  apiRetryRenderer,
  compactBoundaryRenderer,
  compactingRenderer,
  contextClearedRenderer,
  controlResponseRenderer,
  interruptedRenderer,
  microcompactBoundaryRenderer,
  planUpdatedRenderer,
  settingsChangedRenderer,
  systemInitRenderer,
} from '../../notificationRenderers'
import { hasMoreLinesThan } from '../../results/useCollapsedLines'
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
      return assistantTextRenderer.render(parsed, context)
    case 'assistant_thinking':
      return assistantThinkingRenderer.render(parsed, context)
    case 'user_text':
      return userTextContentRenderer.render(parsed, context)
    case 'user_content':
      return userContentRenderer.render(parsed, context)
    case 'plan_execution':
      return planExecutionRenderer.render(parsed, context)
    case 'task_notification':
      return taskNotificationRenderer.render(parsed, context)
    case 'notification':
      return claudeNotificationRenderer(parsed, context)
    case 'result_divider':
      return resultRenderer.render(parsed, context)
    case 'control_response':
      return controlResponseRenderer.render(parsed, context)
    case 'compact_summary':
    case 'hidden':
      return <span />
    case 'unknown':
      return tryClaudeUnknownKindRenderers(parsed, context)
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
  context: RenderContext | undefined,
): JSX.Element | null {
  return settingsChangedRenderer.render(parsed, context)
    ?? interruptedRenderer.render(parsed, context)
    ?? contextClearedRenderer.render(parsed, context)
    ?? compactingRenderer.render(parsed, context)
    ?? agentErrorRenderer.render(parsed, context)
    ?? planUpdatedRenderer.render(parsed, context)
    ?? rateLimitRenderer.render(parsed, context)
    ?? apiRetryRenderer.render(parsed, context)
    ?? compactBoundaryRenderer.render(parsed, context)
    ?? microcompactBoundaryRenderer.render(parsed, context)
    ?? systemInitRenderer.render(parsed, context)
}

/** Collapsed agent prompt view: MessageSquare icon + "Prompt" title + collapsed markdown body. */
function AgentPromptView(props: {
  text: string
  context?: RenderContext
}): JSX.Element {
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.AGENT_PROMPT)
  const isCollapsed = () => !expanded() && hasMoreLinesThan(props.text, COLLAPSED_RESULT_ROWS)
  const html = createMemo(() => renderMarkdown(props.text))

  return (
    <ToolUseLayout
      icon={MessageSquare}
      toolName="Prompt"
      title="Prompt"
      context={props.context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <div class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={html()} />
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

  const text = joinContentParagraphs(content as Array<Record<string, unknown>>, { text: 'text' })
  if (!text)
    return null

  return <AgentPromptView text={text} context={context} />
}
