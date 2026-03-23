/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { AgentChatMessage, AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Bot from 'lucide-solid/icons/bot'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { createSignal, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { createLogger } from '~/lib/logger'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import { thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { isObject } from './messageUtils'
import {
  agentErrorRenderer,
  agentRenamedRenderer,
  compactBoundaryRenderer,
  compactingRenderer,
  contextClearedRenderer,
  controlResponseRenderer,
  interruptedRenderer,
  microcompactBoundaryRenderer,
  rateLimitRenderer,
  resultRenderer,
  settingsChangedRenderer,
  systemInitRenderer,
} from './notificationRenderers'
import { exitPlanModeRenderer, renderExitPlanMode } from './planModeRenderers'
import { getProviderPlugin } from './providers'
import {
  askUserQuestionRenderer,
  renderAskUserQuestion,
  renderTodoWrite,
  taskNotificationRenderer,
  todoWriteRenderer,
} from './taskRenderers'

import {
  ToolHeaderActions,
  toolResultRenderer,
  toolUseRenderer,
} from './toolRenderers'
import {
  toolInputText,
  toolMessage,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'

export { ToolHeaderActions }

const logger = createLogger('messageRenderers')

/** Context passed to renderers from MessageBubble. */
export interface RenderContext {
  /** ISO timestamp of the message (for relative time in toolbar). */
  createdAt?: string
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** User's preferred diff view. */
  diffView?: DiffViewPreference
  /** Copy raw JSON to clipboard. */
  onCopyJson?: () => void
  /** Whether JSON was just copied (for feedback). */
  jsonCopied?: boolean
  /** Parent tool_use name (passed to tool_result renderers for context). */
  parentToolName?: string
  /** Parent tool_use input (passed to tool_result renderers for context). */
  parentToolInput?: Record<string, unknown>
  /** The corresponding tool_use message (looked up by spanId for tool_result messages). */
  toolUseMessage?: AgentChatMessage
  /** Color index assigned to this message's span (−1 = no color). */
  spanColor?: number
  /** Whether the Bash/TaskOutput tool result is expanded (controlled by MessageBubble). */
  toolResultExpanded?: boolean
}

export interface MessageContentRenderer {
  /** Try to render the parsed JSON content. Return null if this renderer doesn't handle it. */
  render: (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null
}

function markdownClass(_role: MessageRole): string {
  return markdownContent
}

// ---------------------------------------------------------------------------
// Specialized tool render functions (accept pre-extracted tool_use data)
// ---------------------------------------------------------------------------

/** Handles assistant messages: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}} */
const assistantTextRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || !isObject(parsed.message))
      return null
    const content = (parsed.message as Record<string, unknown>).content
    if (!Array.isArray(content))
      return null
    const text = content
      .filter((c: unknown) => isObject(c) && c.type === 'text')
      .map((c: unknown) => (c as Record<string, unknown>).text)
      .join('')
    if (!text)
      return null
    return <div class={markdownClass(role)} innerHTML={renderMarkdown(text)} />
  },
}

/** Inner component for thinking messages — owns local expand/collapse state. */
function ThinkingMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  const [expanded, setExpanded] = createSignal(false)

  return (
    <>
      <div class={thinkingHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text="Thinking">
          <span class={inlineFlex}>
            <Icon icon={Brain} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>Thinking</span>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          <div class={markdownContent} innerHTML={renderMarkdown(props.text)} />
        </div>
      </Show>
    </>
  )
}

/** Handles assistant thinking messages: {"type":"assistant","message":{"content":[{"type":"thinking","thinking":"..."}]}} */
const assistantThinkingRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || !isObject(parsed.message))
      return null
    const content = (parsed.message as Record<string, unknown>).content
    if (!Array.isArray(content))
      return null
    const thinkingBlock = content.find(
      (c: unknown) => isObject(c) && c.type === 'thinking',
    ) as Record<string, unknown> | undefined
    if (!thinkingBlock)
      return null
    const text = String(thinkingBlock.thinking || '')
    if (!text)
      return null
    return <ThinkingMessage text={text} context={context} />
  },
}

/** Renders task_started system messages as a minimal "Task started" line (thread child). */
const taskStartedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_started')
      return null

    return (
      <div class={toolMessage}>
        <div class={toolUseHeader}>
          <Tooltip text="Task Started">
            <span class={inlineFlex}>
              <Icon icon={Bot} size="md" class={toolUseIcon} />
            </span>
          </Tooltip>
          <span class={toolInputText}>Task started</span>
        </div>
      </div>
    )
  },
}

/**
 * Handles user messages with string content: {"type":"user","message":{"content":"..."}}
 * This covers local slash command responses (e.g. /context) whose message.content
 * is a plain string rather than an array of content blocks. If the content is
 * wrapped in <local-command-stdout> tags, the inner text is extracted and rendered
 * as markdown.
 */
const userTextContentRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || parsed.type !== 'user')
      return null

    const message = parsed.message as Record<string, unknown>
    if (!isObject(message))
      return null

    const content = message.content
    if (typeof content !== 'string')
      return null

    // Extract text between <local-command-stdout> tags if present.
    const startTag = '<local-command-stdout>'
    const endTag = '</local-command-stdout>'
    const startIdx = content.indexOf(startTag)
    const endIdx = content.indexOf(endTag)
    const text = startIdx !== -1 && endIdx !== -1 && endIdx > startIdx
      ? content.slice(startIdx + startTag.length, endIdx).trim()
      : content

    if (!text)
      return null

    return <div class={markdownClass(role)} innerHTML={renderMarkdown(text)} />
  },
}

/** Handles user messages: {"content":"..."} */
const userContentRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || typeof parsed.content !== 'string' || 'type' in parsed)
      return null
    return <div class={markdownClass(role)} innerHTML={renderMarkdown(parsed.content as string)} />
  },
}

// ---------------------------------------------------------------------------
// Dispatch map — O(1) renderer lookup by MessageCategory kind
// ---------------------------------------------------------------------------

/** Specialized tool renderers keyed by tool name. */
const SPECIALIZED_TOOL_RENDERERS: Record<string, (toolUse: Record<string, unknown>, context?: RenderContext) => JSX.Element | null> = {
  ExitPlanMode: renderExitPlanMode,
  TodoWrite: renderTodoWrite,
  AskUserQuestion: renderAskUserQuestion,
}

/** Dispatch rendering for a tool_use category: try specialized renderer first, then generic. */
function dispatchToolUse(
  category: Extract<MessageCategory, { kind: 'tool_use' }>,
  parsed: unknown,
  role: MessageRole,
  context?: RenderContext,
): JSX.Element | null {
  const specialized = SPECIALIZED_TOOL_RENDERERS[category.toolName]
  if (specialized) {
    const result = specialized(category.toolUse, context)
    if (result !== null)
      return result
  }
  return toolUseRenderer.render(parsed, role, context)
}

/** Renderer functions keyed by MessageCategory kind. */
const KIND_RENDERERS: Record<string, (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null> = {
  // Wrap in arrow functions to avoid accessing cross-module `const` exports
  // at module initialization time, which can hit the TDZ due to the circular
  // dependency between messageRenderers ↔ toolRenderers.
  tool_result: (p, r, c) => toolResultRenderer.render(p, r, c),
  assistant_text: assistantTextRenderer.render,
  assistant_thinking: assistantThinkingRenderer.render,
  user_text: userTextContentRenderer.render,
  user_content: userContentRenderer.render,
  task_notification: taskNotificationRenderer.render,
  notification: (parsed, role, context) => {
    // Try each notification renderer in order
    return settingsChangedRenderer.render(parsed, role, context)
      ?? interruptedRenderer.render(parsed, role, context)
      ?? contextClearedRenderer.render(parsed, role, context)
      ?? compactingRenderer.render(parsed, role, context)
      ?? agentErrorRenderer.render(parsed, role, context)
      ?? agentRenamedRenderer.render(parsed, role, context)
      ?? rateLimitRenderer.render(parsed, role, context)
      ?? compactBoundaryRenderer.render(parsed, role, context)
      ?? microcompactBoundaryRenderer.render(parsed, role, context)
      ?? systemInitRenderer.render(parsed, role, context)
      ?? null
  },
  result_divider: resultRenderer.render,
  control_response: controlResponseRenderer.render,
  compact_summary: () => <span />,
  hidden: () => <span />,
}

/**
 * Dispatch-based rendering using a pre-computed MessageCategory.
 * Returns null only for 'unknown' kind (caller should fall back to linear scan).
 */
function dispatchRender(
  category: MessageCategory,
  parsed: unknown,
  role: MessageRole,
  context?: RenderContext,
): JSX.Element | null {
  if (category.kind === 'tool_use')
    return dispatchToolUse(category, parsed, role, context)

  const renderer = KIND_RENDERERS[category.kind]
  if (renderer)
    return renderer(parsed, role, context)

  return null
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Fallback renderer list for linear scan when O(1) dispatch doesn't match.
 * Lazily initialised on first access to avoid TDZ errors from the circular
 * dependency between messageRenderers ↔ toolRenderers.
 */
let _fallbackRenderers: MessageContentRenderer[] | null = null
function getFallbackRenderers(): MessageContentRenderer[] {
  if (!_fallbackRenderers) {
    _fallbackRenderers = [
      exitPlanModeRenderer,
      todoWriteRenderer,
      askUserQuestionRenderer,
      toolUseRenderer,
      toolResultRenderer,
      userTextContentRenderer,
      assistantTextRenderer,
      assistantThinkingRenderer,
      userContentRenderer,
      taskNotificationRenderer,
      taskStartedRenderer,
      settingsChangedRenderer,
      interruptedRenderer,
      contextClearedRenderer,
      compactingRenderer,
      agentErrorRenderer,
      agentRenamedRenderer,
      rateLimitRenderer,
      compactBoundaryRenderer,
      microcompactBoundaryRenderer,
      systemInitRenderer,
      resultRenderer,
      controlResponseRenderer,
    ]
  }
  return _fallbackRenderers
}

/**
 * Render a message's content.
 *
 * When a `category` is provided (from `classifyMessage()`), rendering uses O(1)
 * dispatch instead of iterating through the renderer chain. The linear scan is
 * used as a fallback for 'unknown' categories and for thread children that don't
 * have a pre-computed category.
 *
 * When `agentProvider` is set and the provider has a `renderMessage` method,
 * that is tried first — allowing providers to render their native message
 * formats without any hardcoded dispatch here.
 */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  role: MessageRole,
  context?: RenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
): JSX.Element {
  try {
    const parsed = typeof parsedOrRawJson === 'string'
      ? JSON.parse(parsedOrRawJson)
      : parsedOrRawJson

    // Fast path: O(1) dispatch when category is available
    if (category && category.kind !== 'unknown') {
      // Try provider-specific renderer first.
      if (agentProvider != null) {
        const plugin = getProviderPlugin(agentProvider)
        if (plugin?.renderMessage) {
          const result = plugin.renderMessage(category, parsed, role, context)
          if (result !== null)
            return result
        }
      }

      const result = dispatchRender(category, parsed, role, context)
      if (result !== null)
        return result
    }

    // Fallback: linear scan through renderer chain
    for (const renderer of getFallbackRenderers()) {
      const result = renderer.render(parsed, role, context)
      if (result !== null)
        return result
    }
  }
  catch (err) { logger.warn('Failed to render message content:', err) }
  return <span>{typeof parsedOrRawJson === 'string' ? parsedOrRawJson : JSON.stringify(parsedOrRawJson)}</span>
}
