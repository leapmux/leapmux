import type { Component } from 'solid-js'
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

interface MessageBubbleProps {
  message: AgentChatMessage
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
  let contentRef: HTMLDivElement | undefined

  // Consolidated message parsing: decompress and parse once, derive everything from this memo.
  const parsed = createMemo((): ParsedMessageContent => parseMessageContent(props.message))

  // Single-pass classification: replaces ~15 individual boolean flags.
  const category = createMemo(() => classifyMessage(parsed().parentObject, parsed().wrapper, props.message.agentProvider))

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

  // Whether the message is rendered by a renderer that has its own internal ToolHeaderActions.
  const hasInternalActions = () => category().kind === 'tool_use' || category().kind === 'tool_result'

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

  // Build render context for message renderers.
  const renderContext = (): RenderContext => ({
    createdAt: props.message.createdAt,
    workingDir: props.workingDir,
    homeDir: props.homeDir,
    diffView: prefs.diffView(),
    onCopyJson: copyJson,
    jsonCopied: jsonCopied(),
    toolUseMessage: toolUseMessage(),
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
  const bubbleClass = () => messageBubbleClass(category().kind, props.message.role)

  onMount(() => {
    if (contentRef)
      injectCopyButtons(contentRef)
  })

  return (
    <Show when={category().kind !== 'hidden'}>
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
    </Show>
  )
}
