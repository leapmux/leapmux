import type { Component } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { RenderContext } from './messageRenderers'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'

import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createSignal, ErrorBoundary, onMount, Show } from 'solid-js'
import { render } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { createLogger } from '~/lib/logger'
import { parseMessageContent } from '~/lib/messageParser'
import { formatChatQuote } from '~/lib/quoteUtils'
import * as styles from './MessageBubble.css'
import { classifyMessage, messageBubbleClass, messageRowClass } from './messageClassification'
import { renderMessageContent, ToolHeaderActions } from './messageRenderers'
import * as chatStyles from './messageStyles.css'
import { getAssistantContent } from './messageUtils'
import { renderNotificationThread } from './notificationRenderers'
import { prettifyJson } from './rendererUtils'
import { COLLAPSED_RESULT_ROWS } from './toolRenderers'

const logger = createLogger('MessageBubble')

function errorDetail(err: unknown): string {
  if (err instanceof Error)
    return err.stack ?? err.message
  return String(err)
}

function renderErrorFallback(label: string) {
  return (err: unknown) => {
    logger.warn(label, err)
    return (
      <span class={chatStyles.systemMessage}>
        {'Failed to render message: '}
        <pre>{errorDetail(err)}</pre>
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

function injectCopyButtons(container: HTMLElement) {
  const preElements = container.querySelectorAll('pre')
  for (const pre of preElements) {
    if (pre.querySelector('.copy-code-button'))
      continue

    const host = document.createElement('div')
    host.style.display = 'contents'

    render(() => {
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

    pre.appendChild(host)
  }
}

/** Classify a message, returning both the parsed content and category. */
export function classifyParsedMessage(message: AgentChatMessage) {
  const parsed = parseMessageContent(message)
  let category = classifyMessage(parsed.parentObject, parsed.wrapper, message.agentProvider)
  // Hide task_started/task_progress system messages inside spans — progress is shown via the span's context.
  if (category.kind === 'notification' && message.parentSpanId
    && (parsed.parentObject?.subtype === 'task_started' || parsed.parentObject?.subtype === 'task_progress')) {
    category = { kind: 'hidden' }
  }
  // Hide ToolSearch tool_use and tool_result — they are internal plumbing, not user-visible.
  if ((category.kind === 'tool_use' || category.kind === 'tool_result') && message.spanType === 'ToolSearch') {
    category = { kind: 'hidden' }
  }
  return { parsed, category }
}

interface MessageBubbleProps {
  message: AgentChatMessage
  parsed?: ParsedMessageContent
  category?: MessageCategory
  error?: string
  onRetry?: () => void
  onDelete?: () => void
  workingDir?: string
  homeDir?: string
  onReply?: (quotedText: string) => void
  /** Look up a message by its spanId (for tool_use ↔ tool_result linking). */
  getMessageBySpanId?: (spanId: string) => AgentChatMessage | undefined
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const [jsonCopied, setJsonCopied] = createSignal(false)
  const [markdownCopied, setMarkdownCopied] = createSignal(false)
  const [toolResultExpanded, setToolResultExpanded] = createSignal(false)
  const [localDiffView, setLocalDiffView] = createSignal<'unified' | 'split' | null>(null)
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

  // Look up the corresponding tool_use message for tool_result messages.
  const toolUseMessage = createMemo(() => {
    if (category().kind !== 'tool_result')
      return undefined
    const spanId = props.message.spanId
    if (!spanId || !props.getMessageBySpanId)
      return undefined
    return props.getMessageBySpanId(spanId)
  })

  // Whether the message is rendered by a renderer that has its own internal ToolHeaderActions.
  // tool_use and agent_prompt render their own ToolHeaderActions inside ToolUseLayout.
  const hasInternalActions = () => category().kind === 'tool_use' || category().kind === 'agent_prompt'

  // Determine if this tool_result is collapsible (enough lines/items to warrant collapse).
  const isCollapsibleToolResult = createMemo(() => {
    if (category().kind !== 'tool_result')
      return false
    const obj = parsed().parentObject
    if (!obj)
      return false

    const toolUseResult = obj.tool_use_result as Record<string, unknown> | undefined
    const toolName = props.message.spanType || String(toolUseResult?.tool_name || '')

    // Grep/Glob: collapsible if filenames array exceeds threshold.
    if (toolName === 'Grep' || toolName === 'Glob') {
      const filenames = Array.isArray(toolUseResult?.filenames) ? toolUseResult!.filenames as string[] : []
      return filenames.length > COLLAPSED_RESULT_ROWS
    }

    // Read with structured file data: use numLines directly.
    if (toolName === 'Read') {
      const file = toolUseResult?.file as Record<string, unknown> | undefined
      if (file && typeof file.numLines === 'number')
        return file.numLines > COLLAPSED_RESULT_ROWS
    }

    // Agent: always collapsible when content is present (uses rem-based height collapse).
    if (toolName === 'Agent') {
      return Array.isArray(toolUseResult?.content)
        && (toolUseResult!.content as Array<Record<string, unknown>>).some(c => typeof c === 'object' && c !== null && c.type === 'text')
    }

    // Bash/Read/TaskOutput/unknown: collapsible if result text exceeds threshold lines.
    if (toolName === 'Bash' || toolName === 'Read' || toolName === 'TaskOutput' || toolName === '') {
      const msg = obj.message as Record<string, unknown> | undefined
      if (!msg || !Array.isArray(msg.content))
        return false
      const tr = (msg.content as Array<Record<string, unknown>>).find(c => c.type === 'tool_result')
      if (!tr)
        return false
      const rc = Array.isArray(tr.content)
        ? (tr.content as Array<Record<string, unknown>>).filter(c => c.type === 'text').map(c => c.text).join('')
        : String(tr.content || '')
      return rc.split('\n').length > COLLAPSED_RESULT_ROWS
    }

    return false
  })

  // Determine if this tool_result has a diff (Edit/Write with structuredPatch or old/new strings).
  const hasToolResultDiff = createMemo(() => {
    if (category().kind !== 'tool_result')
      return false
    const obj = parsed().parentObject
    if (!obj)
      return false
    const toolUseResult = obj.tool_use_result as Record<string, unknown> | undefined
    if (!toolUseResult)
      return false
    if (Array.isArray(toolUseResult.structuredPatch) && (toolUseResult.structuredPatch as unknown[]).length > 0)
      return true
    const oldString = String(toolUseResult.oldString || '')
    const newString = String(toolUseResult.newString || '')
    return oldString !== '' && newString !== '' && oldString !== newString
  })

  const diffView = () => localDiffView() ?? prefs.diffView()
  const toggleDiffView = () => setLocalDiffView(diffView() === 'unified' ? 'split' : 'unified')

  // Build render context for message renderers.
  const renderContext = (): RenderContext => ({
    createdAt: props.message.createdAt,
    workingDir: props.workingDir,
    homeDir: props.homeDir,
    diffView: diffView(),
    onCopyJson: copyJson,
    jsonCopied: jsonCopied(),
    toolUseMessage: toolUseMessage(),
    spanColor: props.message.spanColor,
    spanType: props.message.spanType,
    toolResultExpanded: toolResultExpanded(),
  })

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
    if (cat.kind !== 'user_text' && cat.kind !== 'user_content')
      return null
    const obj = parsed().parentObject
    if (!obj)
      return null
    if (cat.kind === 'user_text') {
      const msg = obj.message as Record<string, unknown> | undefined
      if (typeof msg?.content === 'string')
        return msg.content.trim() || null
    }
    if (cat.kind === 'user_content' && typeof obj.content === 'string')
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
  const bubbleClass = () => isLocalPending() && props.message.role === MessageRole.USER
    ? chatStyles.userMessagePending
    : messageBubbleClass(category().kind, props.message.role)

  onMount(() => {
    if (contentRef)
      injectCopyButtons(contentRef)
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
              {category().kind === 'notification_thread'
                ? renderNotificationThread((category() as { kind: 'notification_thread', messages: unknown[] }).messages)
                : renderMessageContent(parsed().parentObject ?? parsed().rawText, props.message.role, renderContext(), category(), props.message.agentProvider)}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!hasInternalActions()}>
          <ToolHeaderActions
            createdAt={props.message.createdAt}
            onCopyJson={copyJson}
            jsonCopied={jsonCopied()}
            expanded={toolResultExpanded()}
            onToggleExpand={isCollapsibleToolResult() ? () => setToolResultExpanded(v => !v) : undefined}
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
    </div>
  )
}
