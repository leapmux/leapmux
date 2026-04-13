/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { AgentChatMessage, AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'
import Bot from 'lucide-solid/icons/bot'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { createLogger } from '~/lib/logger'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import { attachmentItem, attachmentList, thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { isObject } from './messageUtils'
import {
  agentErrorRenderer,
  agentRenamedRenderer,
  apiRetryRenderer,
  codexMcpStartupStatusRenderer,
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
import { getProviderPlugin } from './providers/registry'
import {
  taskNotificationRenderer,
} from './taskRenderers'
import {
  ToolHeaderActions,
} from './toolRenderers'

import {
  toolInputText,
  toolMessage,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'
import './providers'

export { ToolHeaderActions }

const logger = createLogger('messageRenderers')

/** Context passed to renderers from MessageBubble. */
export interface RenderContext {
  [key: string]: unknown
  /** ISO timestamp of the message (for relative time in toolbar). */
  createdAt?: string
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** User's preferred diff view. */
  diffView?: DiffViewPreference
  /** Reply/quote callback — inserts quoted text into the editor. */
  onReply?: (quotedText: string) => void
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
  /** The corresponding tool_result message (looked up by spanId for tool_use messages). */
  toolResultMessage?: AgentChatMessage
  /** Color index assigned to this message's span (−1 = no color). */
  spanColor?: number
  /** Tool name or item type from span_type column (reliable, always set for span messages). */
  spanType?: string
  /** Current message span id. */
  spanId?: string
  /** Whether the Bash/TaskOutput tool result is expanded (controlled by MessageBubble). */
  toolResultExpanded?: boolean
  /** Live streamed Codex span content for command, fileChange, and reasoning items. */
  commandStream?: CommandStreamSegment[]
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: (key: string) => boolean
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: (key: string, value: boolean) => void
}

export interface MessageContentRenderer {
  /** Try to render the parsed JSON content. Return null if this renderer doesn't handle it. */
  render: (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null
}

export function useSharedExpandedState(
  getContext: () => RenderContext | undefined,
  key: string,
  initial = false,
): [() => boolean, (value: boolean | ((prev: boolean) => boolean)) => void] {
  const [localExpanded, setLocalExpanded] = createSignal(initial)
  const expanded = () => getContext()?.getMessageUiState?.(key) ?? localExpanded()
  const setExpanded = (value: boolean | ((prev: boolean) => boolean)) => {
    const ctx = getContext()
    const next = typeof value === 'function'
      ? (value as (prev: boolean) => boolean)(expanded())
      : value
    if (ctx?.setMessageUiState)
      ctx.setMessageUiState(key, next)
    else
      setLocalExpanded(next)
  }
  return [expanded, setExpanded]
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

/** Collapsible message with icon, label, and markdown body. */
function CollapsibleMessage(props: { text: string, icon: LucideIcon, label: string, stateKey: string, context?: RenderContext }): JSX.Element {
  // eslint-disable-next-line solid/reactivity -- stateKey is a static string, never changes
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, props.stateKey)

  return (
    <>
      <div class={thinkingHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text={props.label} ariaLabel>
          <span class={inlineFlex}>
            <Icon icon={props.icon} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>{props.label}</span>
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

export function ThinkingMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <CollapsibleMessage text={props.text} icon={Brain} label="Thinking" stateKey="thinking" context={props.context} />
}

export function PlanExecutionMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <CollapsibleMessage text={props.text} icon={PlaneTakeoff} label="Execute plan" stateKey="planExecution" context={props.context} />
}

/** Handles plan execution messages: {"content":"...","planExecution":true} */
const planExecutionRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.planExecution !== true)
      return null
    const content = parsed.content as string | undefined
    if (!content)
      return null
    return <PlanExecutionMessage text={content} context={context} />
  },
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
          <Tooltip text="Task Started" ariaLabel>
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

/** Handles user messages: {"content":"..."} or {"content":"...", "attachments":[...]} */
const userContentRenderer: MessageContentRenderer = {
  render(parsed, role, _context) {
    if (!isObject(parsed) || typeof parsed.content !== 'string' || 'type' in parsed)
      return null
    const attachments = Array.isArray((parsed as Record<string, unknown>).attachments)
      ? (parsed as Record<string, unknown>).attachments as Array<{ filename?: string, mime_type?: string }>
      : undefined
    const content = parsed.content as string
    const hasAttachments = attachments && attachments.length > 0
    const hasText = content.trim().length > 0

    if (!hasAttachments) {
      if (!hasText)
        return null
      return <div class={markdownClass(role)} innerHTML={renderMarkdown(content)} />
    }

    return (
      <>
        <div class={attachmentList}>
          <For each={attachments}>
            {att => (
              <span class={attachmentItem}>
                <Icon
                  icon={att.mime_type?.startsWith('image/') ? FileImageIcon : FileIcon}
                  size="xs"
                />
                {att.filename ?? 'Unnamed file'}
              </span>
            )}
          </For>
        </div>
        <Show when={hasText}>
          <div class={markdownClass(role)} innerHTML={renderMarkdown(content)} />
        </Show>
      </>
    )
  },
}

// ---------------------------------------------------------------------------
// Dispatch map — O(1) renderer lookup by MessageCategory kind
// ---------------------------------------------------------------------------

/** Renderer functions keyed by MessageCategory kind. */
const KIND_RENDERERS: Record<string, (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null> = {
  assistant_text: assistantTextRenderer.render,
  assistant_thinking: assistantThinkingRenderer.render,
  user_text: userTextContentRenderer.render,
  user_content: userContentRenderer.render,
  plan_execution: planExecutionRenderer.render,
  task_notification: taskNotificationRenderer.render,
  notification: (parsed, role, context) => {
    // Try each notification renderer in order
    return settingsChangedRenderer.render(parsed, role, context)
      ?? interruptedRenderer.render(parsed, role, context)
      ?? contextClearedRenderer.render(parsed, role, context)
      ?? compactingRenderer.render(parsed, role, context)
      ?? agentErrorRenderer.render(parsed, role, context)
      ?? agentRenamedRenderer.render(parsed, role, context)
      ?? codexMcpStartupStatusRenderer.render(parsed, role, context)
      ?? rateLimitRenderer.render(parsed, role, context)
      ?? apiRetryRenderer.render(parsed, role, context)
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
      codexMcpStartupStatusRenderer,
      rateLimitRenderer,
      apiRetryRenderer,
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
