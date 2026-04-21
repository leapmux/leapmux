import type { Component } from 'solid-js'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createEffect, createMemo, createSignal, For, Match, on, onCleanup, onMount, Show, Switch, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { formatChatQuote } from '~/lib/quoteUtils'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import { spinner } from '~/styles/animations.css'
import * as styles from './ChatView.css'
import { markdownContent } from './markdownContent.css'
import { classifyParsedMessage, MessageBubble } from './MessageBubble'
import { assistantMessage } from './messageStyles.css'
import { SpanLines } from './SpanLines'
import { NO_SPAN_MARGIN } from './SpanLines.css'
import { ThinkingIndicator } from './ThinkingIndicator'
import { ToolUseLayout } from './toolRenderers'

interface ChatViewProps {
  messages: AgentChatMessage[]
  streamingText: string
  /** Whether the agent is actively working (for showing the thinking indicator). */
  agentWorking?: boolean
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
  /** Ref to expose the getScrollState function for viewport save on tab switch. */
  scrollStateRef?: (fn: () => { distFromBottom: number, atBottom: boolean } | undefined) => void
  /** Ref to expose a function that forces an immediate scroll-to-bottom (e.g. when sending a message). */
  scrollToBottomRef?: (fn: () => void) => void
  /** Ref to expose page-wise scrolling for the local chat viewport. */
  pageScrollRef?: (fn: (direction: -1 | 1) => void) => void
  /** Monotonic counter that increments on every addMessage (including thread merges). */
  messageVersion?: number
  /** Called when the user quotes selected text in a chat message. */
  onQuote?: (text: string) => void
  /** Called when the user clicks the reply button on an assistant message. */
  onReply?: (quotedText: string) => void
  /** When "plan", streaming text is rendered with plan styling. */
  streamingType?: string
  /** Look up a message by its spanId (for tool_use ↔ tool_result linking). */
  getMessageBySpanId?: (spanId: string) => AgentChatMessage | undefined
  /** Look up the tool_result message by spanId (reverse of getMessageBySpanId). */
  getToolResultBySpanId?: (spanId: string) => AgentChatMessage | undefined
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
  /** Human-readable label for the agent provider (e.g. "Claude Code"). */
  providerLabel?: string
}

export const ChatView: Component<ChatViewProps> = (props) => {
  const prefs = usePreferences()

  // Lifted expand/collapse state keyed by message ID so that it survives
  // <For> re-renders when new messages are added to the list.
  const [expandedMessages, setExpandedMessages] = createSignal<Set<string>>(new Set())
  const [diffViewOverrides, setDiffViewOverrides] = createSignal<Map<string, 'unified' | 'split'>>(new Map())
  const [messageUiState, setMessageUiState] = createSignal<Map<string, Map<string, boolean>>>(new Map())

  const isMessageExpanded = (messageId: string) => expandedMessages().has(messageId)
  const toggleMessageExpanded = (messageId: string) => {
    setExpandedMessages((prev) => {
      const next = new Set(prev)
      if (next.has(messageId))
        next.delete(messageId)
      else
        next.add(messageId)
      return next
    })
  }
  const getLocalDiffView = (messageId: string) => diffViewOverrides().get(messageId)
  const setLocalDiffView = (messageId: string, view: 'unified' | 'split') => {
    setDiffViewOverrides((prev) => {
      const next = new Map(prev)
      next.set(messageId, view)
      return next
    })
  }
  const getMessageUiBool = (messageId: string, key: string): boolean | undefined =>
    messageUiState().get(messageId)?.get(key)
  const setMessageUiBool = (messageId: string, key: string, value: boolean) => {
    setMessageUiState((prev) => {
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

  let containerRef: HTMLDivElement | undefined
  let messageListRef: HTMLDivElement | undefined
  let contentRef: HTMLDivElement | undefined
  const [atBottom, setAtBottom] = createSignal(true)
  const [preserveBrowsingPosition, setPreserveBrowsingPosition] = createSignal(false)
  let scrollAnimationId: number | null = null
  let suppressAutoLoadOlderAfterRestore = false
  let touchOverscrollStartY: number | null = null
  let pointerOverscrollStartY: number | null = null

  const cancelScrollAnimation = () => {
    if (scrollAnimationId !== null) {
      cancelAnimationFrame(scrollAnimationId)
      scrollAnimationId = null
    }
  }

  /** Fresh DOM measurement — true if the scroll position is at/near the bottom. */
  const isAtBottom = () =>
    !!messageListRef && messageListRef.scrollHeight - messageListRef.scrollTop - messageListRef.clientHeight < 32

  const checkAtBottom = () => {
    if (!messageListRef || scrollAnimationId !== null)
      return
    const nextAtBottom = isAtBottom()
    setAtBottom(nextAtBottom)
    if (nextAtBottom)
      setPreserveBrowsingPosition(false)
  }

  const isNearTop = () =>
    !!messageListRef && messageListRef.scrollTop < messageListRef.clientHeight / 2

  const canLoadOlderMessages = () =>
    !!messageListRef && !!props.hasOlderMessages && !props.fetchingOlder

  const loadOlderMessages = () => {
    if (!canLoadOlderMessages() || !isNearTop())
      return false
    setPreserveBrowsingPosition(true)
    props.onLoadOlderMessages?.()
    return true
  }

  const tryLoadOlderOnExplicitTopIntent = () => {
    if (!messageListRef || messageListRef.scrollTop !== 0)
      return false
    suppressAutoLoadOlderAfterRestore = false
    return loadOlderMessages()
  }

  const handleScroll = () => {
    checkAtBottom()
    if (suppressAutoLoadOlderAfterRestore)
      return
    loadOlderMessages()
  }

  const handleWheel = (event: WheelEvent) => {
    if (event.ctrlKey)
      return
    if (event.deltaY < 0)
      tryLoadOlderOnExplicitTopIntent()
  }

  const handleKeyDown = (event: KeyboardEvent) => {
    if (event.altKey || event.ctrlKey || event.metaKey)
      return
    if (event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'Home')
      tryLoadOlderOnExplicitTopIntent()
  }

  const maybeLoadOlderOnDragAtTop = (startY: number | null, currentY: number) => {
    if (startY === null || !messageListRef || messageListRef.scrollTop !== 0)
      return false
    if (currentY - startY < 12)
      return false
    return tryLoadOlderOnExplicitTopIntent()
  }

  const handleTouchStart = (event: TouchEvent) => {
    touchOverscrollStartY = event.touches[0]?.clientY ?? null
  }

  const handleTouchMove = (event: TouchEvent) => {
    if (maybeLoadOlderOnDragAtTop(touchOverscrollStartY, event.touches[0]?.clientY ?? 0))
      touchOverscrollStartY = null
  }

  const clearTouchOverscroll = () => {
    touchOverscrollStartY = null
  }

  const handlePointerDown = (event: PointerEvent) => {
    if (event.pointerType !== 'mouse')
      pointerOverscrollStartY = event.clientY
  }

  const handlePointerMove = (event: PointerEvent) => {
    if (event.pointerType !== 'mouse' && maybeLoadOlderOnDragAtTop(pointerOverscrollStartY, event.clientY))
      pointerOverscrollStartY = null
  }

  const clearPointerOverscroll = () => {
    pointerOverscrollStartY = null
  }

  // Scroll anchoring: when older messages are prepended, adjust scrollTop
  // so that the viewport stays on the same messages the user was looking at.
  let anchorScrollHeight = 0
  let anchorFirstSeq: bigint | undefined

  createEffect(() => {
    const msgs = props.messages
    if (!messageListRef || msgs.length === 0)
      return
    const newFirstSeq = msgs[0].seq
    if (anchorFirstSeq !== undefined && newFirstSeq < anchorFirstSeq) {
      // Loading older history means the user is browsing away from the bottom.
      // Clear stickiness immediately so a concurrent append cannot snap the
      // view down before the anchor adjustment rAF runs.
      setAtBottom(false)
      setPreserveBrowsingPosition(true)
      const prevHeight = anchorScrollHeight
      requestAnimationFrame(() => {
        if (messageListRef) {
          const delta = messageListRef.scrollHeight - prevHeight
          messageListRef.scrollTop += delta
          // Programmatic viewport anchoring does not reliably emit a scroll
          // event, so recompute the sticky-bottom signal explicitly.
          setAtBottom(isAtBottom())
        }
      })
    }
    anchorScrollHeight = messageListRef.scrollHeight
    anchorFirstSeq = newFirstSeq
  })

  // Expose scroll state for viewport save on tab switch.
  const getScrollState = (): { distFromBottom: number, atBottom: boolean } | undefined => {
    if (!messageListRef || messageListRef.clientHeight === 0)
      return undefined
    return {
      distFromBottom: messageListRef.scrollHeight - messageListRef.scrollTop - messageListRef.clientHeight,
      atBottom: atBottom(),
    }
  }

  const forceScrollToBottom = () => {
    cancelScrollAnimation()
    if (messageListRef)
      messageListRef.scrollTop = messageListRef.scrollHeight
    setAtBottom(true)
    setPreserveBrowsingPosition(false)
  }

  const pageScroll = (direction: -1 | 1) => {
    if (!messageListRef)
      return
    messageListRef.scrollBy({
      top: direction * messageListRef.clientHeight,
      behavior: 'auto',
    })
    messageListRef.dispatchEvent(new Event('scroll'))
  }

  onMount(() => {
    props.scrollStateRef?.(getScrollState)
    props.scrollToBottomRef?.(forceScrollToBottom)
    props.pageScrollRef?.(pageScroll)
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
    const ids = new Set(props.messages.map(msg => msg.id))
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
  type ClassifiedEntry = ReturnType<typeof classifyParsedMessage> & { msg: AgentChatMessage }
  const entryCache = new Map<string, ClassifiedEntry>()
  const hasVisibleMessage = (msg: AgentChatMessage): boolean => {
    const cached = entryCache.get(msg.id)
    if (cached && cached.msg.seq === msg.seq)
      return cached.category.kind !== 'hidden'

    const commandStreamLength = msg.spanId ? (props.getCommandStreamBySpanId?.(msg.spanId).length ?? 0) : 0
    const classified = classifyParsedMessage(msg, {
      hasCommandStream: commandStreamLength > 0,
      commandStreamLength,
    })
    return classified.category.kind !== 'hidden'
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
      const commandStreamLength = msg.spanId ? (props.getCommandStreamBySpanId?.(msg.spanId).length ?? 0) : 0
      const classified = classifyParsedMessage(msg, {
        hasCommandStream: commandStreamLength > 0,
        commandStreamLength,
      })
      const entry = { msg, ...classified }
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

  const scrollToBottom = () => {
    if (!messageListRef)
      return
    cancelScrollAnimation()

    const animate = () => {
      if (!messageListRef) {
        scrollAnimationId = null
        return
      }
      const remaining = messageListRef.scrollHeight - messageListRef.scrollTop - messageListRef.clientHeight
      if (remaining < 1) {
        messageListRef.scrollTop = messageListRef.scrollHeight
        scrollAnimationId = null
        setAtBottom(true)
        setPreserveBrowsingPosition(false)
        return
      }
      const step = remaining > 48 ? remaining * 0.5 : remaining * 0.4
      messageListRef.scrollTop += Math.ceil(step)
      scrollAnimationId = requestAnimationFrame(animate)
    }

    scrollAnimationId = requestAnimationFrame(animate)
  }

  let autoScrollFirstSeq: bigint | undefined

  // Auto-scroll the message list to the bottom when new content arrives
  // and the user is already at (or near) the bottom.
  // messageVersion covers thread merges (tool_use_result merged into an
  // existing tool_use) which don't change messages.length.
  createEffect(() => {
    const firstSeq = props.messages[0]?.seq
    const prependedOlderMessages = autoScrollFirstSeq !== undefined
      && firstSeq !== undefined
      && firstSeq < autoScrollFirstSeq
    autoScrollFirstSeq = firstSeq
    void props.messages.length
    void props.messageVersion
    void props.streamingText
    void props.agentWorking
    if (prependedOlderMessages)
      return
    // Use the atBottom signal (not a fresh DOM check) because by the time
    // this effect runs, SolidJS has already updated the DOM — scrollHeight
    // has grown but scrollTop hasn't, so a fresh measurement would wrongly
    // conclude the user is no longer at the bottom. The signal captures
    // the user's scroll position from before the content changed.
    if (untrack(atBottom) && !untrack(preserveBrowsingPosition) && messageListRef) {
      // Skip scroll when hidden (e.g. inactive tab with display:none).
      // The ResizeObserver will scroll to bottom when the tab becomes visible.
      if (messageListRef.clientHeight === 0)
        return
      // Scroll synchronously — the DOM is already updated by SolidJS,
      // so deferring to rAF would cause one visible frame where the view
      // appears scrolled up before snapping back to bottom.
      messageListRef.scrollTop = messageListRef.scrollHeight
      if (props.messages.length > MAX_LOADED_CHAT_MESSAGES) {
        props.onTrimOldMessages?.()
      }
    }
  })

  // Re-check atBottom after the ResizeObserver clears saved scroll state.
  // Restoration itself is handled exclusively by the ResizeObserver's
  // hidden→visible path so that we avoid a race where this effect runs
  // before the tab is actually hidden (clearing saved state too early).
  createEffect(on(
    () => props.savedViewportScroll,
    (saved) => {
      if (!saved && messageListRef && messageListRef.clientHeight > 0) {
        requestAnimationFrame(() => checkAtBottom())
      }
    },
  ))

  onMount(() => {
    if (!containerRef)
      return
    onCleanup(() => {
      cancelScrollAnimation()
    })
  })

  // Observe size changes on both the scroll container (editor/window resize)
  // and the content wrapper (silent DOM mutations like expand/collapse).
  // Defer scroll adjustments to a rAF to avoid "ResizeObserver loop completed
  // with undelivered notifications" errors when collapsing large elements.
  onMount(() => {
    let resizeRafId = 0
    let prevClientHeight = messageListRef?.clientHeight ?? 0
    const handleResize = () => {
      cancelAnimationFrame(resizeRafId)
      // Capture hidden→visible state and atBottom NOW (in the
      // ResizeObserver callback), before browser scroll-restoration
      // events can fire and corrupt the atBottom signal.
      const ch = messageListRef?.clientHeight ?? 0
      const wasHidden = prevClientHeight === 0 && ch > 0
      const savedAtBottom = atBottom()
      const savedScroll = wasHidden ? props.savedViewportScroll : undefined
      resizeRafId = requestAnimationFrame(() => {
        if (!messageListRef || scrollAnimationId !== null)
          return
        prevClientHeight = ch
        if (wasHidden) {
          // Restore saved viewport scroll if available; otherwise use
          // the atBottom value captured before scroll events could fire.
          if (savedScroll) {
            if (savedScroll.atBottom) {
              messageListRef.scrollTop = messageListRef.scrollHeight
              setAtBottom(true)
              suppressAutoLoadOlderAfterRestore = false
            }
            else {
              const maxScroll = messageListRef.scrollHeight - messageListRef.clientHeight
              const clampedToTop = savedScroll.distFromBottom > maxScroll
              messageListRef.scrollTop = clampedToTop ? 0 : maxScroll - savedScroll.distFromBottom
              setAtBottom(false)
              suppressAutoLoadOlderAfterRestore = clampedToTop
            }
            props.onClearSavedViewportScroll?.()
            return
          }
          if (savedAtBottom) {
            messageListRef.scrollTop = messageListRef.scrollHeight
            setAtBottom(true)
            suppressAutoLoadOlderAfterRestore = false
            return
          }
        }
        suppressAutoLoadOlderAfterRestore = false
        checkAtBottom()
      })
    }
    const observer = new ResizeObserver(handleResize)
    if (messageListRef)
      observer.observe(messageListRef)
    if (contentRef)
      observer.observe(contentRef)
    onCleanup(() => {
      cancelAnimationFrame(resizeRafId)
      observer.disconnect()
    })
  })

  return (
    <div ref={containerRef} class={styles.container} data-testid="chat-container">
      <div class={styles.messageListWrapper}>
        <div
          ref={messageListRef}
          class={styles.messageList}
          data-chat-scroll-container="true"
          tabIndex={0}
          onScroll={handleScroll}
          onWheel={handleWheel}
          onKeyDown={handleKeyDown}
          onTouchStart={handleTouchStart}
          onTouchMove={handleTouchMove}
          onTouchEnd={clearTouchOverscroll}
          onTouchCancel={clearTouchOverscroll}
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={clearPointerOverscroll}
          onPointerCancel={clearPointerOverscroll}
        >
          <Show
            when={hasVisibleEntries() || props.streamingText || props.agentWorking}
            fallback={(
              <Switch fallback={<div class={styles.emptyChat}>Send a message to start</div>}>
                <Match when={props.agentStatus === AgentStatus.STARTING}>
                  <div class={styles.emptyChat} data-testid="agent-startup-overlay">
                    <StartupSpinner label={`Starting ${props.providerLabel ?? 'agent'}…`} />
                  </div>
                </Match>
                <Match when={props.agentStatus === AgentStatus.STARTUP_FAILED}>
                  <div
                    class={styles.emptyChat}
                    data-testid="agent-startup-error"
                    style={{ color: 'var(--danger)' }}
                  >
                    <StartupErrorBody
                      title={`${props.providerLabel ?? 'Agent'} failed to start`}
                      error={props.startupError ?? ''}
                    />
                  </div>
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
              <div ref={contentRef} class={styles.messageListContent}>
                <For each={visibleEntries()}>
                  {({ msg, parsed, category }) => {
                    const spanLines = createMemo(() => {
                      if (!msg.spanLines || msg.spanLines === '[]')
                        return []
                      try {
                        return JSON.parse(msg.spanLines)
                      }
                      catch {
                        return []
                      }
                    })

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
                        getMessageBySpanId={props.getMessageBySpanId}
                        getToolResultBySpanId={props.getToolResultBySpanId}
                        commandStream={props.getCommandStreamBySpanId?.(msg.spanId)}
                        toolResultExpanded={isMessageExpanded(msg.id)}
                        onToggleToolResultExpanded={() => toggleMessageExpanded(msg.id)}
                        localDiffView={getLocalDiffView(msg.id)}
                        onSetLocalDiffView={view => setLocalDiffView(msg.id, view)}
                        getMessageUiState={key => getMessageUiBool(msg.id, key)}
                        setMessageUiState={(key, value) => setMessageUiBool(msg.id, key, value)}
                      />
                    )

                    return (
                      <Show
                        when={spanLines().length > 0}
                        fallback={<div data-seq={msg.seq.toString()} style={{ 'margin-left': `${NO_SPAN_MARGIN}px` }}>{bubble}</div>}
                      >
                        <div data-seq={msg.seq.toString()} class={`${styles.messageRow} ${styles.messageRowWithSpanLines}`}>
                          <SpanLines lines={spanLines()} spanOpener={!!msg.spanId} />
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
                  onExpandTick={() => {
                    if (isAtBottom())
                      messageListRef!.scrollTop = messageListRef!.scrollHeight
                  }}
                />
                {/* Keep the startup panel visible below queued messages
                    until the agent transitions out of STARTING/FAILED.
                    The fallback branch above covers the empty state; this
                    handles the case where the user already typed a
                    message (which flips the outer Show). */}
                <Switch>
                  <Match when={props.agentStatus === AgentStatus.STARTING}>
                    <div class={styles.startupPanelInline} data-testid="agent-startup-overlay">
                      <StartupSpinner label={`Starting ${props.providerLabel ?? 'agent'}…`} />
                    </div>
                  </Match>
                  <Match when={props.agentStatus === AgentStatus.STARTUP_FAILED}>
                    <div
                      class={styles.startupPanelInline}
                      data-testid="agent-startup-error"
                      style={{ color: 'var(--danger)' }}
                    >
                      <StartupErrorBody
                        title={`${props.providerLabel ?? 'Agent'} failed to start`}
                        error={props.startupError ?? ''}
                      />
                    </div>
                  </Match>
                </Switch>
              </div>
            </SelectionQuotePopover>
          </Show>
        </div>
        <Show when={!atBottom()}>
          <button type="button" class={`outline icon ${styles.scrollToBottomButton}`} onClick={scrollToBottom}>
            <Icon icon={ArrowDown} size="lg" />
          </button>
        </Show>
      </div>
    </div>
  )
}
