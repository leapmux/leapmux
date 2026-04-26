import type { Component } from 'solid-js'
import type { MessageUiKey } from './messageUiKeys'
import type { ChatScrollState } from './useChatScroll'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { CommandStreamSegment } from '~/stores/chat.store'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createEffect, createMemo, createSignal, For, Match, onCleanup, onMount, Show, Switch } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { formatChatQuote } from '~/lib/quoteUtils'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { spinner } from '~/styles/animations.css'
import { AgentStartupBanner } from './AgentStartupBanner'
import * as styles from './ChatView.css'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { classifyParsedMessage, MessageBubble } from './MessageBubble'
import { assistantMessage } from './messageStyles.css'
import { ToolUseLayout } from './toolRenderers'
import { useChatScroll } from './useChatScroll'
import { SpanLines } from './widgets/SpanLines'
import { NO_SPAN_MARGIN } from './widgets/SpanLines.css'
import { ThinkingIndicator } from './widgets/ThinkingIndicator'

/** Imperative scroll API published by ChatView via `onScrollApiReady`. */
export interface ChatScrollApi {
  getScrollState: () => ChatScrollState | undefined
  forceScrollToBottom: () => void
  pageScroll: (direction: -1 | 1) => void
}

interface ChatViewProps {
  messages: AgentChatMessage[]
  streamingText: string
  /** Whether the agent is actively working (for showing the thinking indicator). */
  agentWorking?: boolean
  /**
   * Whether this ChatView is the active tab in its tile. Forwarded to the
   * thinking indicator so it can suspend its compass simulation when the
   * user can't see it (every agent tab is mounted, including hidden ones).
   */
  tabActive?: boolean
  messageErrors?: Record<string, string>
  /** Per-message non-error sublabels (e.g. "Queued — agent is starting…"). */
  messagePendingLabels?: Record<string, string>
  onRetryMessage?: (messageId: string) => void
  onDeleteMessage?: (messageId: string) => void
  /** Workspace working directory for relativizing file paths in tool messages. */
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** Whether there are older messages available to fetch. */
  hasOlderMessages?: boolean
  /** Whether a fetch for older messages is in progress. */
  fetchingOlder?: boolean
  /** Called when the user scrolls near the top and older messages should be loaded. */
  onLoadOlderMessages?: () => void
  /** Called to trim old messages when total exceeds threshold. */
  onTrimOldMessages?: () => void
  /** Saved scroll state for viewport restoration on tab switch. */
  savedViewportScroll?: { distFromBottom: number, atBottom: boolean }
  /** Called when saved scroll state should be cleared after restoration. */
  onClearSavedViewportScroll?: () => void
  /**
   * Receives the imperative scroll API once the chat viewport mounts.
   * The host (TileRenderer) needs this for tab-switch viewport save,
   * send-message scroll-to-bottom, and keyboard PageUp/PageDown.
   */
  onScrollApiReady?: (api: ChatScrollApi) => void
  /** Monotonic counter that increments on every addMessage (including thread merges). */
  messageVersion?: number
  /** Called when the user quotes selected text in a chat message. */
  onQuote?: (text: string) => void
  /** Called when the user clicks the reply button on an assistant message. */
  onReply?: (quotedText: string) => void
  /** When "plan", streaming text is rendered with plan styling. */
  streamingType?: string
  /** Look up the parsed tool_use message by spanId (for tool_use ↔ tool_result linking). */
  getToolUseParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /** Symmetric counterpart: look up the parsed tool_result message by spanId. */
  getToolResultParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /** Look up live Codex span stream segments by span id. */
  getCommandStreamBySpanId?: (spanId: string) => CommandStreamSegment[]
  /**
   * Agent status. STARTING shows a loader with the provider name in
   * the empty-state area; STARTUP_FAILED shows the server error in
   * --danger. The editor beneath remains interactive during STARTING
   * so the user can type ahead.
   */
  agentStatus?: AgentStatus
  /** Error text from the backend's AgentStatusChange.startup_error. */
  startupError?: string
  /** Phase label from AgentStatusChange.startup_message while STARTING (e.g. "Checking Git status…"). */
  startupMessage?: string
  /** Human-readable label for the agent provider (e.g. "Claude Code"). */
  providerLabel?: string
}

function parseSpanLines(raw: string | undefined): (SpanLine | null)[] {
  if (!raw || raw === '[]')
    return []
  try {
    return JSON.parse(raw) as (SpanLine | null)[]
  }
  catch {
    return []
  }
}

export const ChatView: Component<ChatViewProps> = (props) => {
  const prefs = usePreferences()

  // Lifted per-message UI state keyed by message ID so that it survives
  // <For> re-renders when new messages are added to the list.
  const [diffViewOverrides, setDiffViewOverrides] = createSignal<Map<string, 'unified' | 'split'>>(new Map())
  const [messageUiState, setMessageUiState] = createSignal<Map<string, Map<string, boolean>>>(new Map())

  const getLocalDiffView = (messageId: string) => diffViewOverrides().get(messageId)
  const setLocalDiffView = (messageId: string, view: 'unified' | 'split') => {
    setDiffViewOverrides((prev) => {
      const next = new Map(prev)
      next.set(messageId, view)
      return next
    })
  }
  const getMessageUiBool = (messageId: string, key: MessageUiKey): boolean | undefined =>
    messageUiState().get(messageId)?.get(key)
  const setMessageUiBool = (messageId: string, key: MessageUiKey, value: boolean) => {
    setMessageUiState((prev) => {
      // Skip the Map clones when nothing actually changes — avoids notifying
      // downstream consumers when an `onClick` toggles back to its prior value.
      if (prev.get(messageId)?.get(key) === value)
        return prev
      const next = new Map(prev)
      const current = new Map(next.get(messageId) ?? [])
      current.set(key, value)
      next.set(messageId, current)
      return next
    })
  }

  // Throttle streaming text markdown rendering to animation frames to avoid
  // running the full remark+shiki pipeline on every streaming chunk.
  const [renderedStreamHtml, setRenderedStreamHtml] = createSignal('')
  let streamRafId: number | null = null

  createEffect(() => {
    const text = props.streamingText
    if (!text) {
      if (streamRafId !== null) {
        cancelAnimationFrame(streamRafId)
        streamRafId = null
      }
      setRenderedStreamHtml('')
      return
    }
    if (streamRafId !== null)
      cancelAnimationFrame(streamRafId)
    streamRafId = requestAnimationFrame(() => {
      streamRafId = null
      setRenderedStreamHtml(renderMarkdown(text, true))
    })
  })

  onCleanup(() => {
    if (streamRafId !== null)
      cancelAnimationFrame(streamRafId)
  })

  let contentRef: HTMLDivElement | undefined

  const scroll = useChatScroll({
    messages: () => props.messages,
    messageVersion: () => props.messageVersion,
    streamingText: () => props.streamingText,
    agentWorking: () => props.agentWorking,
    hasOlderMessages: () => props.hasOlderMessages,
    fetchingOlder: () => props.fetchingOlder,
    onLoadOlderMessages: () => props.onLoadOlderMessages?.(),
    onTrimOldMessages: () => props.onTrimOldMessages?.(),
    savedViewportScroll: () => props.savedViewportScroll,
    onClearSavedViewportScroll: () => props.onClearSavedViewportScroll?.(),
  })

  onMount(() => {
    props.onScrollApiReady?.({
      getScrollState: scroll.getScrollState,
      forceScrollToBottom: scroll.forceScrollToBottom,
      pageScroll: scroll.pageScroll,
    })
  })

  let prevMessageCount = 0
  createEffect(() => {
    const count = props.messages.length
    // Messages were only added or updated — no cleanup needed.
    if (count >= prevMessageCount) {
      prevMessageCount = count
      return
    }
    prevMessageCount = count
    const ids = new Set<string>()
    for (const msg of props.messages)
      ids.add(msg.id)
    setMessageUiState((prev) => {
      if (prev.size === 0)
        return prev
      let changed = false
      const next = new Map<string, Map<string, boolean>>()
      for (const [messageId, state] of prev) {
        if (ids.has(messageId))
          next.set(messageId, state)
        else
          changed = true
      }
      return changed ? next : prev
    })
  })

  // Cache classified entries by message ID so that <For> receives stable
  // object references for unchanged messages, avoiding full DOM recreation.
  type ClassifiedEntry = ReturnType<typeof classifyParsedMessage> & {
    msg: AgentChatMessage
    parsedSpanLines: (SpanLine | null)[]
  }
  const entryCache = new Map<string, ClassifiedEntry>()
  const buildEntry = (msg: AgentChatMessage, cached?: ClassifiedEntry): ClassifiedEntry => {
    const commandStreamLength = msg.spanId ? (props.getCommandStreamBySpanId?.(msg.spanId).length ?? 0) : 0
    const classified = classifyParsedMessage(msg, {
      hasCommandStream: commandStreamLength > 0,
      commandStreamLength,
    })
    // Reuse the cached parse when only `seq` bumped but `spanLines` text is identical.
    const parsedSpanLines = cached && cached.msg.spanLines === msg.spanLines
      ? cached.parsedSpanLines
      : parseSpanLines(msg.spanLines)
    return { msg, ...classified, parsedSpanLines }
  }
  const hasVisibleMessage = (msg: AgentChatMessage): boolean => {
    const cached = entryCache.get(msg.id)
    if (cached && cached.msg.seq === msg.seq)
      return cached.category.kind !== 'hidden'
    // Populate the cache so the visibleEntries memo doesn't re-classify.
    const entry = buildEntry(msg, cached)
    entryCache.set(msg.id, entry)
    return entry.category.kind !== 'hidden'
  }
  const visibleEntries = createMemo(() => {
    const showHidden = prefs.showHiddenMessages()
    const newCache = new Map<string, ClassifiedEntry>()
    const result = props.messages.map((msg) => {
      const cached = entryCache.get(msg.id)
      // Reuse cached entry if the message hasn't changed (same seq = same content).
      if (cached && cached.msg.seq === msg.seq) {
        newCache.set(msg.id, cached)
        return cached
      }
      const entry = buildEntry(msg, cached)
      newCache.set(msg.id, entry)
      return entry
    }).filter(entry => showHidden || entry.category.kind !== 'hidden')
    // Replace cache with new entries (drops removed messages).
    entryCache.clear()
    for (const [k, v] of newCache)
      entryCache.set(k, v)
    return result
  })
  // Intentionally separate from visibleEntries() — this memo short-circuits
  // via Array.some() and avoids classifying every message, which is cheaper
  // than materializing the full visibleEntries() array just to check emptiness.
  const hasVisibleEntries = createMemo(() => {
    if (prefs.showHiddenMessages())
      return props.messages.length > 0
    return props.messages.some(msg => hasVisibleMessage(msg))
  })

  return (
    <div class={styles.container} data-testid="chat-container">
      <div class={styles.messageListWrapper}>
        <div
          ref={scroll.attachListRef}
          class={styles.messageList}
          data-chat-scroll-container="true"
          tabIndex={0}
          {...scroll.handlers}
        >
          {/*
            AgentStartupBanner is rendered in two places below: once in the
            empty-state fallback and once trailing the message list. They
            are NOT redundant — the outer <Show> only renders one branch at
            a time, so at most one banner is in the DOM for any given state.
          */}
          <Show
            when={hasVisibleEntries() || props.streamingText || props.agentWorking}
            fallback={(
              <Switch fallback={<div class={styles.emptyChat}>Send a message to start</div>}>
                <Match when={props.agentStatus === AgentStatus.STARTING || props.agentStatus === AgentStatus.STARTUP_FAILED}>
                  <AgentStartupBanner
                    status={props.agentStatus}
                    providerLabel={props.providerLabel}
                    startupError={props.startupError}
                    startupMessage={props.startupMessage}
                    containerClass={styles.emptyChat}
                  />
                </Match>
              </Switch>
            )}
          >
            <Show when={props.fetchingOlder}>
              <div class={styles.loadingOlderIndicator}>
                <Icon icon={LoaderCircle} size="sm" class={spinner} />
                Loading older messages...
              </div>
            </Show>
            <div class={styles.messageListSpacer} />
            <SelectionQuotePopover
              containerRef={contentRef}
              onQuote={text => props.onQuote?.(formatChatQuote(text))}
            >
              <div
                ref={(el) => {
                  contentRef = el
                  scroll.attachContentRef(el)
                }}
                class={styles.messageListContent}
              >
                <For each={visibleEntries()}>
                  {({ msg, parsed, category, parsedSpanLines }) => {
                    const bubble = (
                      <MessageBubble
                        message={msg}
                        parsed={parsed}
                        category={category}
                        error={props.messageErrors?.[msg.id]}
                        pendingLabel={props.messagePendingLabels?.[msg.id]}
                        onRetry={() => props.onRetryMessage?.(msg.id)}
                        onDelete={() => props.onDeleteMessage?.(msg.id)}
                        workingDir={props.workingDir}
                        homeDir={props.homeDir}
                        onReply={props.onReply}
                        host={{
                          getToolUseParsedBySpanId: props.getToolUseParsedBySpanId,
                          getToolResultParsedBySpanId: props.getToolResultParsedBySpanId,
                          commandStream: () => props.getCommandStreamBySpanId?.(msg.spanId),
                          localDiffView: getLocalDiffView(msg.id),
                          onSetLocalDiffView: view => setLocalDiffView(msg.id, view),
                          getMessageUiState: key => getMessageUiBool(msg.id, key),
                          setMessageUiState: (key, value) => setMessageUiBool(msg.id, key, value),
                        }}
                      />
                    )

                    return (
                      <Show
                        when={parsedSpanLines.length > 0}
                        fallback={<div data-seq={msg.seq.toString()} style={{ 'margin-left': `${NO_SPAN_MARGIN}px` }}>{bubble}</div>}
                      >
                        <div data-seq={msg.seq.toString()} class={`${styles.messageRow} ${styles.messageRowWithSpanLines}`}>
                          <SpanLines lines={parsedSpanLines} spanOpener={!!msg.spanId} />
                          <div class={styles.messageRowContent}>
                            {bubble}
                          </div>
                        </div>
                      </Show>
                    )
                  }}
                </For>
                <Show when={props.streamingText}>
                  <Show
                    when={props.streamingType === 'plan'}
                    fallback={(
                      <div class={assistantMessage}>
                        {/* eslint-disable-next-line solid/no-innerhtml -- streaming text rendered via remark */}
                        <div class={markdownContent} innerHTML={renderedStreamHtml()} />
                      </div>
                    )}
                  >
                    <ToolUseLayout
                      icon={PlaneTakeoff}
                      toolName="Plan"
                      title="Proposed Plan"
                      alwaysVisible={true}
                      bordered={false}
                    >
                      <>
                        <hr />
                        {/* eslint-disable-next-line solid/no-innerhtml -- streaming text rendered via remark */}
                        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderedStreamHtml()} />
                      </>
                    </ToolUseLayout>
                  </Show>
                </Show>
                <ThinkingIndicator
                  visible={props.agentWorking ?? false}
                  paused={props.tabActive === false}
                  onExpandTick={() => {
                    if (scroll.isAtBottomFresh())
                      scroll.jumpToBottom()
                  }}
                />
                <AgentStartupBanner
                  status={props.agentStatus}
                  providerLabel={props.providerLabel}
                  startupError={props.startupError}
                  startupMessage={props.startupMessage}
                  containerClass={styles.startupPanelInline}
                />
              </div>
            </SelectionQuotePopover>
          </Show>
        </div>
        <Show when={!scroll.atBottom()}>
          <button type="button" class={`outline icon ${styles.scrollToBottomButton}`} onClick={scroll.scrollToBottomAnimated}>
            <Icon icon={ArrowDown} size="lg" />
          </button>
        </Show>
      </div>
    </div>
  )
}
