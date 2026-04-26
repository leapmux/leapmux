import type { Component } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { RenderContext } from './messageRenderers'
import type { ToolResultMeta } from './providers/registry'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { CommandStreamSegment } from '~/stores/chat.store'

import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createResource, createSignal, ErrorBoundary, onCleanup, onMount, Show } from 'solid-js'
import { render } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { prettifyJson } from '~/lib/jsonFormat'
import { createLogger } from '~/lib/logger'
import { parseMessageContent } from '~/lib/messageParser'
import { formatChatQuote } from '~/lib/quoteUtils'
import { resolveStack } from '~/lib/resolveStack'
import { ACP_SESSION_UPDATE } from '~/types/toolMessages'
import * as styles from './MessageBubble.css'
import { buildClassificationInput, classifyMessage, messageBubbleClass, messageRowClass } from './messageClassification'
import { renderMessageContent } from './messageRenderers'
import * as chatStyles from './messageStyles.css'
import { getAssistantContent } from './messageUtils'
import { renderNotificationThread } from './notificationRenderers'
import { getProviderPlugin } from './providers/registry'
import { ToolHeaderActions } from './toolRenderers'

const logger = createLogger('MessageBubble')

function renderErrorFallback(label: string) {
  return (err: unknown) => {
    logger.warn(label, err)
    const message = err instanceof Error ? err.message : String(err)
    const rawStack = err instanceof Error ? err.stack : undefined
    const [resolved] = createResource(
      () => rawStack,
      stack => stack ? resolveStack(stack) : Promise.resolve(undefined),
    )
    return (
      <span class={chatStyles.systemMessage}>
        {'Failed to render message: '}
        {message}
        <Show when={resolved() ?? rawStack}>
          {stack => <pre>{stack()}</pre>}
        </Show>
      </span>
    )
  }
}

function roleLabel(role: MessageRole): string {
  switch (role) {
    case MessageRole.USER: return 'user'
    case MessageRole.ASSISTANT: return 'assistant'
    case MessageRole.SYSTEM: return 'system'
    case MessageRole.RESULT: return 'result'
    case MessageRole.LEAPMUX: return 'leapmux'
    default: return 'system'
  }
}

function injectCopyButtons(container: HTMLElement): Array<() => void> {
  const disposers: Array<() => void> = []
  const preElements = container.querySelectorAll('pre')
  for (const pre of preElements) {
    if (pre.querySelector('.copy-code-button'))
      continue
    // Skip shiki <pre> inside tool messages — copy is handled by ToolHeaderActions.
    if (pre.closest('[data-tool-message]'))
      continue

    const host = document.createElement('div')
    host.style.display = 'contents'

    const dispose = render(() => {
      const [copied, setCopied] = createSignal(false)

      return (
        <IconButton
          class="copy-code-button"
          icon={copied() ? Check : Copy}
          title={copied() ? 'Copied' : 'Copy'}
          onClick={() => {
            const code = pre.querySelector('code')
            const text = code?.textContent || pre.textContent || ''
            navigator.clipboard.writeText(text).then(() => {
              setCopied(true)
              setTimeout(setCopied, 2000, false)
            })
          }}
        />
      )
    }, host)

    disposers.push(dispose)
    pre.appendChild(host)
  }
  return disposers
}

/** Classify a message, returning both the parsed content and category. */
export function classifyParsedMessage(
  message: AgentChatMessage,
  classificationContext?: { hasCommandStream?: boolean, commandStreamLength?: number },
) {
  const parsed = parseMessageContent(message)
  const category = classifyMessage(buildClassificationInput(parsed, message), classificationContext)
  return { parsed, category }
}

interface MessageBubbleProps {
  message: AgentChatMessage
  parsed?: ParsedMessageContent
  category?: MessageCategory
  error?: string
  /**
   * Non-error pending label rendered beneath the bubble — used for
   * optimistic user messages held in the per-agent startup queue while
   * the agent's subprocess is still starting.
   */
  pendingLabel?: string
  onRetry?: () => void
  onDelete?: () => void
  workingDir?: string
  homeDir?: string
  onReply?: (quotedText: string) => void
  /** Look up the parsed tool_use message by spanId (for tool_result → tool_use linking). */
  getToolUseParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /** Look up the parsed tool_result message by spanId (for tool_use → tool_result linking). */
  getToolResultParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  commandStream?: CommandStreamSegment[]
  /** Lifted expand/collapse state for tool results, managed by ChatView. */
  toolResultExpanded?: boolean
  /** Toggle the expand/collapse state for this message's tool result. */
  onToggleToolResultExpanded?: () => void
  /** Lifted per-message diff view override, managed by ChatView. */
  localDiffView?: 'unified' | 'split'
  /** Set the per-message diff view override. */
  onSetLocalDiffView?: (view: 'unified' | 'split') => void
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: (key: string) => boolean | undefined
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: (key: string, value: boolean) => void
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const [jsonCopied, setJsonCopied] = createSignal(false)
  const [markdownCopied, setMarkdownCopied] = createSignal(false)
  const toolResultExpanded = () => props.toolResultExpanded ?? false
  const localDiffView = () => props.localDiffView
  let contentRef: HTMLDivElement | undefined

  // Use pre-computed values from ChatView when available, otherwise compute on demand.
  const classified = createMemo(() => props.parsed && props.category
    ? { parsed: props.parsed, category: props.category }
    : classifyParsedMessage(props.message))
  const parsed = () => classified().parsed
  const category = () => classified().category

  // Full raw JSON for the Raw JSON display (only stringified on demand for "Copy Raw JSON").
  const rawJson = (): string => {
    const p = parsed()
    const msg = props.message
    const envelope: Record<string, unknown> = {
      id: msg.id,
      role: roleLabel(msg.role),
      seq: Number(msg.seq),
      created_at: msg.createdAt,
    }
    if (msg.deliveryError)
      envelope.delivery_error = msg.deliveryError
    if (msg.depth)
      envelope.depth = msg.depth
    if (msg.spanId)
      envelope.span_id = msg.spanId
    if (msg.parentSpanId)
      envelope.parent_span_id = msg.parentSpanId
    if (msg.spanType)
      envelope.span_type = msg.spanType
    if (msg.spanColor > 0)
      envelope.span_color = msg.spanColor
    if (msg.spanLines && msg.spanLines !== '[]')
      envelope.span_lines = JSON.parse(msg.spanLines)
    if (p.wrapper && p.wrapper.old_seqs.length > 0)
      envelope.old_seqs = p.wrapper.old_seqs

    if (p.wrapper) {
      envelope.messages = p.wrapper.messages
      return JSON.stringify(envelope)
    }

    try {
      envelope.content = JSON.parse(p.rawText)
      return JSON.stringify(envelope)
    }
    catch {
      return p.rawText
    }
  }

  const copyJson = async () => {
    await navigator.clipboard.writeText(prettifyJson(rawJson()))
    setJsonCopied(true)
    setTimeout(setJsonCopied, 2000, false)
  }

  // Look up the parsed sibling tool_use for tool_result bubbles.
  const toolUseParsed = createMemo(() => {
    if (category().kind !== 'tool_result')
      return undefined
    const spanId = props.message.spanId
    if (!spanId || !props.getToolUseParsedBySpanId)
      return undefined
    return props.getToolUseParsedBySpanId(spanId)
  })

  // Look up the parsed sibling tool_result for tool_use bubbles.
  const toolResultParsed = createMemo(() => {
    if (category().kind !== 'tool_use')
      return undefined
    const spanId = props.message.spanId
    if (!spanId || !props.getToolResultParsedBySpanId)
      return undefined
    return props.getToolResultParsedBySpanId(spanId)
  })

  // Toolbar metadata for the current message — collapsibility, diff presence,
  // and a lazy copyable-content getter. Each provider's plugin decides which
  // messages produce metadata (Claude returns it for tool_result; Codex for
  // terminal-state tool_use spans).
  const toolMeta = createMemo<ToolResultMeta | null>(() => {
    const plugin = getProviderPlugin(props.message.agentProvider)
    if (!plugin?.toolResultMeta)
      return null
    return plugin.toolResultMeta(category(), parsed().parentObject, props.message.spanType, toolUseParsed())
  })

  // The renderer renders its own ToolHeaderActions (inside ToolUseLayout) for
  // tool_use / agent_prompt — except when the provider produces toolResultMeta
  // for a tool_use, which means the message is acting as a result and uses the
  // bubble's outer toolbar instead.
  const hasInternalActions = () => {
    const kind = category().kind
    if (kind !== 'tool_use' && kind !== 'agent_prompt')
      return false
    return toolMeta() === null
  }

  const isCollapsibleToolResult = () => toolMeta()?.collapsible ?? false
  const hasToolResultDiff = () => toolMeta()?.hasDiff ?? false
  const copyableResultContent = () => toolMeta()?.copyableContent() ?? null

  const [resultCopied, setResultCopied] = createSignal(false)
  const copyResultContent = async () => {
    const content = copyableResultContent()
    if (!content)
      return
    await navigator.clipboard.writeText(content)
    setResultCopied(true)
    setTimeout(setResultCopied, 2000, false)
  }

  const diffView = () => localDiffView() ?? prefs.diffView()
  const toggleDiffView = () => props.onSetLocalDiffView?.(diffView() === 'unified' ? 'split' : 'unified')

  // Build render context for message renderers. A plain object literal with
  // getter accessors for reactive fields gives stable identity (allocated once
  // per component setup) AND per-field reactivity — body components track only
  // the getters they read, so changes to one field don't cascade to siblings.
  const renderContext: RenderContext = {
    get workingDir() { return props.workingDir },
    get homeDir() { return props.homeDir },
    diffView,
    // eslint-disable-next-line solid/reactivity -- props.onReply is read each call via the getter, not captured here
    get onReply() { return props.onReply ? (text: string) => props.onReply!(formatChatQuote(text)) : undefined },
    onCopyJson: copyJson,
    jsonCopied,
    get createdAt() { return props.message.createdAt },
    get expandAgentThoughts() { return prefs.expandAgentThoughts() },
    get toolUseParsed() { return toolUseParsed() },
    get toolResultParsed() { return toolResultParsed() },
    get spanColor() { return props.message.spanColor },
    get spanType() { return props.message.spanType },
    get spanId() { return props.message.spanId },
    toolResultExpanded,
    commandStream: () => props.commandStream,
    get getMessageUiState() { return props.getMessageUiState },
    get setMessageUiState() { return props.setMessageUiState },
  }

  // Extract assistant text for the reply button.
  const extractAssistantText = (): string | null => {
    const cat = category()
    if (cat.kind !== 'assistant_text' && cat.kind !== 'assistant_thinking')
      return null
    const obj = parsed().parentObject
    if (!obj)
      return null

    // Codex format: {item: {type: 'agentMessage', text: '...'}, ...}
    const item = obj.item as Record<string, unknown> | undefined
    if (item?.type === 'agentMessage' && typeof item.text === 'string')
      return item.text.trim() || null

    // OpenCode format: {sessionUpdate: 'agent_message_chunk'|'agent_thought_chunk', content: {text: '...'}}
    const su = obj.sessionUpdate as string | undefined
    if (su === ACP_SESSION_UPDATE.AGENT_MESSAGE_CHUNK || su === ACP_SESSION_UPDATE.AGENT_THOUGHT_CHUNK) {
      const ct = obj.content as Record<string, unknown> | undefined
      if (ct && typeof ct.text === 'string')
        return (ct.text as string).trim() || null
    }

    // Claude Code format: {type: 'assistant', message: {content: [...]}}
    const content = getAssistantContent(obj)
    if (!content)
      return null
    return content
      .filter(c => c.type === 'text' || c.type === 'thinking')
      .map(c => String(c.type === 'thinking' ? (c as Record<string, unknown>).thinking || '' : c.text || ''))
      .join('\n')
      .trim() || null
  }

  // Extract user text for the quote button.
  const extractUserText = (): string | null => {
    const cat = category()
    if (cat.kind !== 'user_text' && cat.kind !== 'user_content' && cat.kind !== 'plan_execution')
      return null
    const obj = parsed().parentObject
    if (!obj)
      return null
    if (cat.kind === 'user_text') {
      const msg = obj.message as Record<string, unknown> | undefined
      if (typeof msg?.content === 'string')
        return msg.content.trim() || null
    }
    if ((cat.kind === 'user_content' || cat.kind === 'plan_execution') && typeof obj.content === 'string')
      return (obj.content as string).trim() || null
    return null
  }

  const extractQuotableText = () => extractAssistantText() ?? extractUserText()

  const handleReply = () => {
    const text = extractQuotableText()
    if (text && props.onReply) {
      props.onReply(formatChatQuote(text))
    }
  }

  const copyMarkdown = async () => {
    const text = extractQuotableText()
    if (!text)
      return
    await navigator.clipboard.writeText(text)
    setMarkdownCopied(true)
    setTimeout(setMarkdownCopied, 2000, false)
  }

  const rowClass = () => messageRowClass(category().kind, props.message.role)
  const isLocalPending = () => props.message.id.startsWith('local-')
  const isPendingUserMessage = () => isLocalPending() && props.message.role === MessageRole.USER && !props.error
  const bubbleClass = () => isPendingUserMessage()
    ? chatStyles.userMessagePending
    : messageBubbleClass(category().kind, props.message.role)

  onMount(() => {
    if (!contentRef)
      return
    const disposers = injectCopyButtons(contentRef)
    onCleanup(() => {
      for (const d of disposers)
        d()
    })
  })

  return (
    <div
      class={props.error ? styles.messageWithError : undefined}
      style={!props.error ? { display: 'contents' } : undefined}
    >
      <div class={rowClass()}>
        <div
          class={bubbleClass()}
          data-testid="message-bubble"
          data-role={roleLabel(props.message.role)}
        >
          <div ref={contentRef} data-testid="message-content">
            <ErrorBoundary fallback={renderErrorFallback('Failed to render message:')}>
              {category().kind === 'hidden'
                ? <pre class={chatStyles.hiddenMessageJson}>{prettifyJson(rawJson())}</pre>
                : category().kind === 'notification_thread'
                  ? renderNotificationThread((category() as { kind: 'notification_thread', messages: unknown[] }).messages, props.message.agentProvider)
                  : renderMessageContent(parsed().parentObject ?? parsed().rawText, props.message.role, renderContext, category(), props.message.agentProvider)}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!hasInternalActions()}>
          <ToolHeaderActions
            createdAt={props.message.createdAt}
            onCopyJson={copyJson}
            jsonCopied={jsonCopied()}
            onCopyContent={copyableResultContent() ? copyResultContent : undefined}
            contentCopied={resultCopied()}
            expanded={toolResultExpanded()}
            onToggleExpand={isCollapsibleToolResult() ? props.onToggleToolResultExpanded : undefined}
            hasDiff={hasToolResultDiff()}
            diffView={diffView()}
            onToggleDiffView={hasToolResultDiff() ? toggleDiffView : undefined}
            onReply={extractQuotableText() ? handleReply : undefined}
            onCopyMarkdown={extractQuotableText() ? copyMarkdown : undefined}
            markdownCopied={markdownCopied()}
          />
        </Show>
      </div>

      <Show when={props.error}>
        <div class={styles.messageError} data-testid="message-error">
          <span class={styles.messageErrorText}>Failed to deliver</span>
          <span class={styles.messageErrorDot}>&middot;</span>
          <button class={styles.messageRetryButton} onClick={() => props.onRetry?.()} data-testid="message-retry-button">Retry</button>
          <span class={styles.messageErrorDot}>&middot;</span>
          <button class={styles.messageDeleteButton} onClick={() => props.onDelete?.()} data-testid="message-delete-button">Delete</button>
        </div>
      </Show>
      <Show when={!props.error && props.pendingLabel}>
        <div class={styles.messageError} data-testid="message-pending">
          <span class={styles.messageErrorText} style={{ color: 'inherit', opacity: '0.7' }}>{props.pendingLabel}</span>
        </div>
      </Show>
    </div>
  )
}
