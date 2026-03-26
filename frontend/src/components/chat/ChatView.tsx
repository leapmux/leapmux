import type { Component } from 'solid-js'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createEffect, createMemo, createSignal, For, on, onCleanup, onMount, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
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
  /** Look up live Codex span stream segments by span id. */
  getCommandStreamBySpanId?: (spanId: string) => CommandStreamSegment[]
}

export const ChatView: Component<ChatViewProps> = (props) => {
  const prefs = usePreferences()

  // Lifted expand/collapse state keyed by message ID so that it survives
  // <For> re-renders when new messages are added to the list.
  const [expandedMessages, setExpandedMessages] = createSignal<Set<string>>(new Set())
  const [diffViewOverrides, setDiffViewOverrides] = createSignal<Map<string, 'unified' | 'split'>>(new Map())

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
  let scrollAnimationId: number | null = null
  let autoScrollPending = false

  const cancelScrollAnimation = () => {
    if (scrollAnimationId !== null) {
      cancelAnimationFrame(scrollAnimationId)
      scrollAnimationId = null
    }
  }

  const checkAtBottom = () => {
    if (!messageListRef || scrollAnimationId !== null || autoScrollPending)
      return
    setAtBottom(messageListRef.scrollHeight - messageListRef.scrollTop - messageListRef.clientHeight < 32)
  }

  const handleScroll = () => {
    checkAtBottom()
    if (messageListRef && props.hasOlderMessages && !props.fetchingOlder) {
      if (messageListRef.scrollTop < messageListRef.clientHeight / 2) {
        props.onLoadOlderMessages?.()
      }
    }
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
      const prevHeight = anchorScrollHeight
      requestAnimationFrame(() => {
        if (messageListRef) {
          const delta = messageListRef.scrollHeight - prevHeight
          messageListRef.scrollTop += delta
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
  }

  onMount(() => {
    props.scrollStateRef?.(getScrollState)
    props.scrollToBottomRef?.(forceScrollToBottom)
  })

  const visibleEntries = createMemo(() => {
    const showHidden = prefs.showHiddenMessages()
    return props.messages.map((msg) => {
      let classified = classifyParsedMessage(msg)
      if (
        classified.category.kind === 'hidden'
        && msg.agentProvider === AgentProvider.CODEX
        && msg.spanType === 'reasoning'
        && msg.spanId
        && (props.getCommandStreamBySpanId?.(msg.spanId).length ?? 0) > 0
      ) {
        classified = { ...classified, category: { kind: 'assistant_thinking' } }
      }
      return { msg, ...classified }
    }).filter(entry => showHidden || entry.category.kind !== 'hidden')
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
        return
      }
      const step = remaining > 48 ? remaining * 0.5 : remaining * 0.4
      messageListRef.scrollTop += Math.ceil(step)
      scrollAnimationId = requestAnimationFrame(animate)
    }

    scrollAnimationId = requestAnimationFrame(animate)
  }

  // Auto-scroll the message list to the bottom when new content arrives
  // and the user is already at (or near) the bottom.
  // messageVersion covers thread merges (tool_use_result merged into an
  // existing tool_use) which don't change messages.length.
  createEffect(() => {
    void props.messages.length
    void props.messageVersion
    void props.streamingText
    void props.agentWorking
    if (untrack(atBottom) && messageListRef) {
      // Skip scroll when hidden (e.g. inactive tab with display:none).
      // The ResizeObserver will scroll to bottom when the tab becomes visible.
      if (messageListRef.clientHeight === 0)
        return
      autoScrollPending = true
      requestAnimationFrame(() => {
        messageListRef!.scrollTop = messageListRef!.scrollHeight
        setAtBottom(true)
        autoScrollPending = false
      })
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
        if (!messageListRef || scrollAnimationId !== null || autoScrollPending)
          return
        prevClientHeight = ch
        if (wasHidden) {
          // Restore saved viewport scroll if available; otherwise use
          // the atBottom value captured before scroll events could fire.
          if (savedScroll) {
            if (savedScroll.atBottom) {
              messageListRef.scrollTop = messageListRef.scrollHeight
              setAtBottom(true)
            }
            else {
              const maxScroll = messageListRef.scrollHeight - messageListRef.clientHeight
              messageListRef.scrollTop = savedScroll.distFromBottom > maxScroll ? 0 : maxScroll - savedScroll.distFromBottom
              setAtBottom(false)
            }
            props.onClearSavedViewportScroll?.()
            return
          }
          if (savedAtBottom) {
            messageListRef.scrollTop = messageListRef.scrollHeight
            setAtBottom(true)
            return
          }
        }
        if (atBottom()) {
          messageListRef.scrollTop = messageListRef.scrollHeight
        }
        else {
          checkAtBottom()
        }
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
        <div ref={messageListRef} class={styles.messageList} onScroll={handleScroll}>
          <Show
            when={props.messages.length > 0 || props.streamingText || props.agentWorking}
            fallback={<div class={styles.emptyChat}>Send a message to start</div>}
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
                        onRetry={() => props.onRetryMessage?.(msg.id)}
                        onDelete={() => props.onDeleteMessage?.(msg.id)}
                        workingDir={props.workingDir}
                        homeDir={props.homeDir}
                        onReply={props.onReply}
                        getMessageBySpanId={props.getMessageBySpanId}
                        commandStream={props.getCommandStreamBySpanId?.(msg.spanId)}
                        toolResultExpanded={isMessageExpanded(msg.id)}
                        onToggleToolResultExpanded={() => toggleMessageExpanded(msg.id)}
                        localDiffView={getLocalDiffView(msg.id)}
                        onSetLocalDiffView={view => setLocalDiffView(msg.id, view)}
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
                  visible={props.agentWorking && !props.streamingText}
                  onExpandTick={() => {
                    if (atBottom() && messageListRef) {
                      messageListRef.scrollTop = messageListRef.scrollHeight
                    }
                  }}
                />
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
