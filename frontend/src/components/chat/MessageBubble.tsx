import type { Component } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiWriteOptions, RenderContext } from './messageRenderers'
import type { MessageUiKey } from './messageUiKeys'
import type { ToolResultMeta } from './providers/registry'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/stores/chatTodos'
import type { CommandStreamSegment } from '~/stores/chatTypes'

import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createEffect, createMemo, createResource, ErrorBoundary, onCleanup, onMount, Show, untrack } from 'solid-js'
import { render } from 'solid-js/web'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { formatErrorMessage } from '~/lib/errors'
import { cancelIdle, requestIdle } from '~/lib/idleCallback'
import { prettifyJson } from '~/lib/jsonFormat'
import { createLogger } from '~/lib/logger'
import { formatChatQuote } from '~/lib/quoteUtils'
import { resolveStack } from '~/lib/resolveStack'
import { buildRawJsonEnvelope } from './chatRawJson'
import { codeCopyHostClass } from './markdownEditor/markdownContent.css'
import * as styles from './MessageBubble.css'
import { classifyParsedMessage, messageBubbleClass, messageRowClass } from './messageClassification'
import { renderMessageContent } from './messageRenderers'
import * as chatStyles from './messageStyles.css'
import { expandedUiKeyFor, MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'
import { renderNotificationThread } from './notificationRenderers'
import { providerFor } from './providers/registry'
import { renderResultDivider } from './resultDividerRenderers'
import { JsonHighlightHtml, ToolHeaderActions } from './toolRenderers'

const logger = createLogger('MessageBubble')

function renderErrorFallback(label: string) {
  return (err: unknown) => {
    logger.warn(label, err)
    const message = formatErrorMessage(err)
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

function sourceLabel(source: MessageSource): string {
  switch (source) {
    case MessageSource.USER: return 'user'
    case MessageSource.AGENT: return 'agent'
    case MessageSource.LEAPMUX: return 'leapmux'
    // Only MESSAGE_SOURCE_UNSPECIFIED (proto 0) reaches here, and every
    // persistence path sets a real source -- an UNSPECIFIED row is a
    // misconfigured agent-side persistence bug. Surface it as 'unknown' (a
    // visibly anomalous data-role) instead of silently masquerading as 'agent',
    // matching the no-guessing stance for an unknown agentProvider.
    default: return 'unknown'
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
    // The raw hidden-message JSON needs no guard here: it now renders as token <span>s
    // (JsonHighlightHtml), not a <pre>, so this querySelectorAll('pre') sweep never sees
    // it. Copy for that block is handled by the row's ToolHeaderActions.

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
    // Mark the <pre> so the copy-button positioning (absolute, top-right) and its
    // relative anchor apply regardless of where the <pre> lives -- a markdown body or a
    // non-markdown block (e.g. a result-divider error <pre>). Without this the button
    // anchored only inside `.markdownContent` and fell inline elsewhere.
    pre.classList.add(codeCopyHostClass)
    pre.appendChild(host)
  }
  return disposers
}

/**
 * ChatView-owned bindings exposed to a MessageBubble. Grouped here so the
 * bubble has a single host-side prop instead of a sprawling list of lifted
 * callbacks. Every field is optional — a bubble rendered outside ChatView
 * (tests, isolated previews) can pass `host={undefined}`.
 */
export interface MessageBubbleHost {
  /** O(1) live-todo lookup for this bubble's agent (resolves subjects for status-only TaskUpdate patches). */
  getTodoById?: (taskId: string) => TodoItem | undefined
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
  setMessageUiState?: (key: MessageUiKey, value: boolean, opts?: MessageUiWriteOptions) => void
  /** Debug: this row's measured DOM height, for the raw-JSON surface. */
  getHeightDebug?: () => { measured?: number }
  /** Per-row/content-version cache for pure renderer derivations shared across hidden + visible mounts. */
  renderCache?: MessageRenderCache
  /** True while visible row rendering should avoid starting syntax-highlight jobs. */
  syntaxHighlightingPaused?: () => boolean
  /** True while the user has a live document selection inside the chat content. */
  textSelectionActive?: () => boolean
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
  /** Hidden premeasurement pass: keep layout structure, skip interactive/expensive chrome. */
  premeasureMode?: boolean
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const toolResultExpanded = () =>
    props.host?.getMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
    ?? messageUiDefault(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
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
  // The raw-JSON debug envelope (hidden / unsupported_provider rows). The pure
  // builder lives in chatRawJson so its proto-field copying and parse-failure
  // fallbacks are unit-testable without mounting a component.
  const rawJson = (): string =>
    buildRawJsonEnvelope(props.message, parsed(), sourceLabel(props.message.source), props.host?.getHeightDebug?.())

  const { copied: jsonCopied, copy: copyJson } = useCopyButton(() => props.premeasureMode ? undefined : prettifyJson(rawJson()))

  // Reactive, memoized pretty raw JSON for the displayed block. Gated on the only two
  // categories that render it (hidden / unsupported_provider) so the proto-envelope
  // build + FracturedJson reformat stays lazy for every other bubble (matching rawJson's
  // plain-function intent), while the rows that DO show it reformat once per change
  // instead of on every TokenizedCode `props.code` read (2-3x per reactive pass).
  const prettyRawJson = createMemo(() => {
    const kind = category().kind
    if (kind !== 'hidden' && kind !== 'unsupported_provider')
      return ''
    return prettifyJson(rawJson())
  })

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

  const { copied: resultCopied, copy: copyResultContent } = useCopyButton(() => props.premeasureMode ? undefined : toolMeta()?.copyableContent() ?? undefined)

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
    get getTodoById() { return props.host?.getTodoById },
    get workingDir() { return props.workingDir },
    get homeDir() { return props.homeDir },
    diffView,
    get onReply() { return wrappedOnReply() },
    get onCopyJson() { return copyJson },
    get jsonCopied() { return props.premeasureMode ? () => false : jsonCopied },
    get createdAt() { return props.message.createdAt },
    get expandAgentThoughts() { return prefs.expandAgentThoughts() },
    // Resolve the row's expand-toggle UI key ONCE here (kind + provider), so the
    // thinking-style renderers read the same key ChatView resolved -- see
    // expandedUiKeyFor. Getter so the literal stays referentially stable while
    // tracking a category/provider change.
    get expandUiKey() { return expandedUiKeyFor(category().kind, props.message.agentProvider) },
    get toolUseParsed() { return toolUseParsed() },
    get toolResultParsed() { return toolResultParsed() },
    get renderCache() { return props.host?.renderCache },
    syntaxHighlightingPaused: () => props.host?.syntaxHighlightingPaused?.() ?? false,
    textSelectionActive: () => props.host?.textSelectionActive?.() ?? false,
    get spanColor() { return props.message.spanColor },
    get spanType() { return props.message.spanType },
    get spanId() { return props.message.spanId },
    commandStream: () => props.host?.commandStream?.(),
    get getMessageUiState() { return props.host?.getMessageUiState },
    get setMessageUiState() { return props.premeasureMode ? undefined : props.host?.setMessageUiState },
    get premeasureMode() { return props.premeasureMode === true },
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

  const { copied: markdownCopied, copy: copyMarkdown } = useCopyButton(() => props.premeasureMode ? undefined : extractQuotableText() ?? undefined)

  const rowClass = () => messageRowClass(category().kind, props.message.source)
  const isLocalPending = () => props.message.id.startsWith('local-')
  const isPendingUserMessage = () => isLocalPending() && props.message.source === MessageSource.USER && !props.error
  const bubbleClass = () => isPendingUserMessage()
    ? chatStyles.userMessagePending
    : messageBubbleClass(category().kind, props.message.source)

  // A notification category carries the messages to render -- a consolidated
  // thread holds the wrapper's messages; a standalone notification is a
  // one-element thread (classify supplies `[parentObject]`). The narrowing here
  // is just for the type; a non-notification category never reaches this.
  const notificationMessages = (): unknown[] => {
    const cat = category()
    return cat.kind === 'notification' ? cat.messages : []
  }

  // The payload to hand a renderer: the parsed parent object, or the raw text
  // when the envelope didn't parse to an object. Named once so renderContent and
  // the result_divider arm pass the renderers the same shape.
  const renderPayload = () => parsed().parentObject ?? parsed().rawText

  // Render the message body through the provider plugin, ending in the raw-JSON
  // last-resort span when no renderer claims it. Used for every non-notification
  // category, and as the fallback when a notification produces no entries (an
  // unrecognized or legacy shape) -- so the message surfaces as raw JSON rather
  // than an empty bubble.
  const renderContent = () =>
    renderMessageContent(renderPayload(), renderContext, category(), props.message.agentProvider)

  // The raw-JSON last-resort block (highlighted as token spans via the async
  // token worker), shared by the `hidden` category and the unsupported-provider
  // error surface so the rendering lives in one place and can't drift. Copy is
  // handled by the row's ToolHeaderActions; rendering token <span>s (not a
  // shiki <pre>) means the markdown copy-button injector never targets it.
  const rawJsonBlock = () => (
    <JsonHighlightHtml class={chatStyles.hiddenMessageJson} code={prettyRawJson()} context={renderContext} />
  )

  // Loud surface for a message whose `agentProvider` is UNSPECIFIED or has no
  // registered plugin (classify returns `unsupported_provider`). We refuse to
  // guess another provider's renderer, so show an explicit error plus the raw
  // JSON for debugging -- a visible misconfiguration, not a silent mis-render.
  const renderUnsupportedProvider = () => (
    <>
      <div style={{ color: 'var(--danger)' }}>
        {`Unsupported agent provider: ${agentProviderLabel(props.message.agentProvider)} (${props.message.agentProvider}) -- cannot render this message.`}
      </div>
      {rawJsonBlock()}
    </>
  )

  onMount(() => {
    if (props.premeasureMode)
      return
    if (!contentRef)
      return
    const el = contentRef
    let disposers: Array<() => void> = []
    let idle: number | undefined
    let observer: MutationObserver | undefined
    let reinjectAfterSelection = false
    const isTextSelectionActive = () => untrack(() => props.host?.textSelectionActive?.() ?? false)
    const disposeAll = () => {
      for (const d of disposers)
        d()
      disposers = []
    }
    // (Re-)inject copy buttons over the CURRENT content. Disconnect the observer across
    // the injection so our own appendChild(host) writes don't re-trigger it (which would
    // loop). Dispose the prior roots first: an async re-render replaces the markdown
    // <div>'s innerHTML wholesale, orphaning the old buttons' Solid roots, so we drop them
    // and re-apply to the new <pre> elements.
    const reinject = () => {
      observer?.disconnect()
      disposeAll()
      disposers = injectCopyButtons(el)
      observer?.observe(el, { childList: true, subtree: true })
    }
    // Defer (re-)injection to idle: the querySelectorAll('pre') + per-<pre> button render
    // is post-mount chrome, not part of the first paint. A row that flings past unmounts
    // and cancels the handle before it fires, so the work is skipped for rows that scroll
    // by and runs only for rows that settle visible. The debounce also coalesces the burst
    // of mutations from one re-render into a single re-injection.
    const schedule = () => {
      if (isTextSelectionActive()) {
        reinjectAfterSelection = true
        return
      }
      if (idle !== undefined)
        cancelIdle(idle)
      idle = requestIdle(() => {
        idle = undefined
        if (isTextSelectionActive()) {
          reinjectAfterSelection = true
          return
        }
        reinject()
      })
    }
    createEffect(() => {
      if (props.host?.textSelectionActive?.())
        return
      if (!reinjectAfterSelection)
        return
      reinjectAfterSelection = false
      schedule()
    })
    // Re-apply on a CONTENT change, not just the first render: a code block's body is
    // produced by renderMarkdown, whose syntax highlighting now lands ASYNCHRONOUSLY (the
    // worker's highlighted HTML replaces the plain placeholder's innerHTML, wiping the
    // injected buttons). A one-shot injection raced that swap -- inject before it and the
    // button is wiped; after it and the button lands -- so a code block "sometimes" had no
    // copy button. Observing contentRef re-injects after the swap (and after streaming
    // re-renders / expand-collapse) regardless of timing.
    //
    // But IGNORE mutations the copy buttons cause themselves -- the IconButton swapping its
    // Copy<->Check icon (and title) when clicked is a subtree mutation. Re-injecting on
    // that would dispose the button mid-click, wiping its transient "Copied" checkmark and
    // churning every button in the bubble on each copy. Re-inject only when a mutation
    // touches something OUTSIDE the copy-button chrome.
    const onMutations = (records: MutationRecord[]) => {
      for (const r of records) {
        const node = r.target
        const asEl = node instanceof Element ? node : node.parentElement
        if (!asEl?.closest('.copy-code-button')) {
          schedule()
          return
        }
      }
    }
    observer = new MutationObserver(onMutations)
    observer.observe(el, { childList: true, subtree: true })
    schedule()
    onCleanup(() => {
      observer?.disconnect()
      if (idle !== undefined)
        cancelIdle(idle)
      disposeAll()
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
          data-role={sourceLabel(props.message.source)}
        >
          <div ref={contentRef} data-testid="message-content">
            <ErrorBoundary fallback={renderErrorFallback('Failed to render message:')}>
              {category().kind === 'hidden'
                ? rawJsonBlock()
                : category().kind === 'notification'
                  ? (renderNotificationThread(notificationMessages(), props.message.agentProvider) ?? renderContent())
                  : category().kind === 'result_divider'
                    ? (renderResultDivider(renderPayload(), props.message.agentProvider) ?? renderContent())
                    : category().kind === 'unsupported_provider'
                      ? renderUnsupportedProvider()
                      : renderContent()}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!hasInternalActions()}>
          <ToolHeaderActions
            caller={{
              onCopyContent: hasCopyableResult() ? copyResultContent : undefined,
              contentCopied: resultCopied(),
              onReply: extractQuotableText() ? (props.premeasureMode ? () => {} : handleReply) : undefined,
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
