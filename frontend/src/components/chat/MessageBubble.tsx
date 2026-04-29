import type { Component } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { RenderContext } from './messageRenderers'
import type { MessageUiKey } from './messageUiKeys'
import type { ToolResultMeta } from './providers/registry'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { CommandStreamSegment } from '~/stores/chat.store'

import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createResource, ErrorBoundary, onCleanup, onMount, Show } from 'solid-js'
import { render } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import { createLogger } from '~/lib/logger'
import { parseMessageContent } from '~/lib/messageParser'
import { formatChatQuote } from '~/lib/quoteUtils'
import { resolveStack } from '~/lib/resolveStack'
import * as styles from './MessageBubble.css'
import { buildClassificationInput, classifyMessage, messageBubbleClass, messageRowClass } from './messageClassification'
import { renderMessageContent } from './messageRenderers'
import * as chatStyles from './messageStyles.css'
import { MESSAGE_UI_KEY } from './messageUiKeys'
import { renderNotificationThread } from './notificationRenderers'
import { providerFor } from './providers/registry'
import { renderJsonHighlight, ToolHeaderActions } from './toolRenderers'

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
    case MessageRole.TURN_END: return 'result'
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
    // Skip raw hidden-message JSON — copy is handled by the row's ToolHeaderActions.
    // closest() walks ancestors so the marker can sit on a wrapper around a Shiki <pre>.
    if (pre.closest('[data-code-copy="false"]'))
      continue

    const host = document.createElement('div')
    host.style.display = 'contents'

    const dispose = render(() => {
      const { copied, copy } = useCopyButton(() => {
        const code = pre.querySelector('code')
        return code?.textContent || pre.textContent || ''
      })
      return (
        <IconButton
          class="copy-code-button"
          icon={copied() ? Check : Copy}
          title={copied() ? 'Copied' : 'Copy'}
          onClick={copy}
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

/**
 * ChatView-owned bindings exposed to a MessageBubble. Grouped here so the
 * bubble has a single host-side prop instead of a sprawling list of lifted
 * callbacks. Every field is optional — a bubble rendered outside ChatView
 * (tests, isolated previews) can pass `host={undefined}`.
 */
export interface MessageBubbleHost {
  /** Look up the parsed tool_use message by spanId (for tool_result → tool_use linking). */
  getToolUseParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /** Look up the parsed tool_result message by spanId (for tool_use → tool_result linking). */
  getToolResultParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /**
   * Live command stream for this message's span, as a thunk so the host
   * literal stays cheap to construct: callers do the lookup only when a
   * renderer reads it.
   */
  commandStream?: () => CommandStreamSegment[] | undefined
  /** Lifted per-message diff view override, managed by ChatView. */
  localDiffView?: 'unified' | 'split'
  /** Set the per-message diff view override. */
  onSetLocalDiffView?: (view: 'unified' | 'split') => void
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: (key: MessageUiKey) => boolean | undefined
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: (key: MessageUiKey, value: boolean) => void
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
  /** Lifted state and lookups owned by the parent ChatView. */
  host?: MessageBubbleHost
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const toolResultExpanded = () =>
    props.host?.getMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED) ?? false
  const toggleToolResultExpanded = () =>
    props.host?.setMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, !toolResultExpanded())
  let contentRef: HTMLDivElement | undefined

  // Use pre-computed values from ChatView when available, otherwise compute on demand.
  const classified = createMemo(() => props.parsed && props.category
    ? { parsed: props.parsed, category: props.category }
    : classifyParsedMessage(props.message))
  const parsed = () => classified().parsed
  const category = () => classified().category

  // Full raw JSON for the Raw JSON display. Plain function (not createMemo)
  // so the JSON.parse + JSON.stringify only run when a consumer actually
  // reads it (Copy Raw JSON click, hidden-message <pre> render).
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

  const { copied: jsonCopied, copy: copyJson } = useCopyButton(() => prettifyJson(rawJson()))

  // Look up the parsed sibling tool_use for tool_result bubbles.
  const toolUseParsed = createMemo(() => {
    if (category().kind !== 'tool_result')
      return undefined
    const spanId = props.message.spanId
    const lookup = props.host?.getToolUseParsedBySpanId
    if (!spanId || !lookup)
      return undefined
    return lookup(spanId)
  })

  // Look up the parsed sibling tool_result for tool_use bubbles.
  const toolResultParsed = createMemo(() => {
    if (category().kind !== 'tool_use')
      return undefined
    const spanId = props.message.spanId
    const lookup = props.host?.getToolResultParsedBySpanId
    if (!spanId || !lookup)
      return undefined
    return lookup(spanId)
  })

  // Toolbar metadata for the current message — collapsibility, diff presence,
  // and a lazy copyable-content getter. Each provider's plugin decides which
  // messages produce metadata (Claude returns it for tool_result; Codex for
  // terminal-state tool_use spans).
  const toolMeta = createMemo<ToolResultMeta | null>(() => {
    const plugin = providerFor(props.message.agentProvider)
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
  const hasCopyableResult = () => toolMeta()?.hasCopyable ?? false

  const { copied: resultCopied, copy: copyResultContent } = useCopyButton(() => toolMeta()?.copyableContent() ?? undefined)

  const diffView = () => props.host?.localDiffView ?? prefs.diffView()
  const toggleDiffView = () => props.host?.onSetLocalDiffView?.(diffView() === 'unified' ? 'split' : 'unified')

  // Memoize the wrapped onReply so callers reading `context.onReply` get a
  // stable reference between renders. Recomputes only when `props.onReply`
  // identity changes.
  const wrappedOnReply = createMemo(() => {
    const onReply = props.onReply
    return onReply ? (text: string) => onReply(formatChatQuote(text)) : undefined
  })

  // Build render context for message renderers. A plain object literal with
  // getter accessors for reactive fields gives stable identity (allocated once
  // per component setup) AND per-field reactivity — body components track only
  // the getters they read, so changes to one field don't cascade to siblings.
  const renderContext: RenderContext = {
    get workingDir() { return props.workingDir },
    get homeDir() { return props.homeDir },
    diffView,
    get onReply() { return wrappedOnReply() },
    onCopyJson: copyJson,
    jsonCopied,
    get createdAt() { return props.message.createdAt },
    get expandAgentThoughts() { return prefs.expandAgentThoughts() },
    get toolUseParsed() { return toolUseParsed() },
    get toolResultParsed() { return toolResultParsed() },
    get spanColor() { return props.message.spanColor },
    get spanType() { return props.message.spanType },
    get spanId() { return props.message.spanId },
    commandStream: () => props.host?.commandStream?.(),
    get getMessageUiState() { return props.host?.getMessageUiState },
    get setMessageUiState() { return props.host?.setMessageUiState },
  }

  // Quotable text dispatch: each provider plugin reads its own wire format
  // (Codex: parent.item.text, ACP: parent.content.text, Claude: message.content[]).
  const extractQuotableText = createMemo(() => {
    const plugin = providerFor(props.message.agentProvider)
    return plugin?.extractQuotableText?.(category(), parsed()) ?? null
  })

  const handleReply = () => {
    const text = extractQuotableText()
    if (text && props.onReply) {
      props.onReply(formatChatQuote(text))
    }
  }

  const { copied: markdownCopied, copy: copyMarkdown } = useCopyButton(() => extractQuotableText() ?? undefined)

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
                ? (
                    // eslint-disable-next-line solid/no-innerhtml -- HTML is produced via shiki, not arbitrary user input
                    <div class={chatStyles.hiddenMessageJson} data-code-copy="false" innerHTML={renderJsonHighlight(prettifyJson(rawJson()))} />
                  )
                : category().kind === 'notification_thread'
                  ? renderNotificationThread((category() as { kind: 'notification_thread', messages: unknown[] }).messages, props.message.agentProvider)
                  : renderMessageContent(parsed().parentObject ?? parsed().rawText, props.message.role, renderContext, category(), props.message.agentProvider)}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!hasInternalActions()}>
          <ToolHeaderActions
            caller={{
              onCopyContent: hasCopyableResult() ? copyResultContent : undefined,
              contentCopied: resultCopied(),
              onReply: extractQuotableText() ? handleReply : undefined,
              onCopyMarkdown: extractQuotableText() ? copyMarkdown : undefined,
              markdownCopied: markdownCopied(),
            }}
            layout={{
              createdAt: props.message.createdAt,
              expanded: toolResultExpanded(),
              onToggleExpand: isCollapsibleToolResult() ? toggleToolResultExpanded : undefined,
              onCopyJson: copyJson,
              jsonCopied: jsonCopied(),
              hasDiff: hasToolResultDiff(),
              diffView: diffView(),
              onToggleDiffView: hasToolResultDiff() ? toggleDiffView : undefined,
            }}
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
