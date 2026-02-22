import type { Component } from 'solid-js'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'

import ArrowDown from 'lucide-solid/icons/arrow-down'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createSignal, For, on, onCleanup, onMount, Show, untrack } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { spinner } from '~/styles/animations.css'
import { iconSize } from '~/styles/tokens'
import * as styles from './ChatView.css'
import { markdownContent } from './markdownContent.css'
import { MessageBubble } from './MessageBubble'
import { assistantMessage } from './messageStyles.css'
import { ThinkingIndicator } from './ThinkingIndicator'

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
}

export const ChatView: Component<ChatViewProps> = (props) => {
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
    setAtBottom(messageListRef.scrollHeight - messageListRef.scrollTop - messageListRef.clientHeight < 16)
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

  onMount(() => {
    props.scrollStateRef?.(getScrollState)
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
  createEffect(() => {
    void props.messages.length
    void props.streamingText
    void props.agentWorking
    if (untrack(atBottom) && messageListRef) {
      autoScrollPending = true
      requestAnimationFrame(() => {
        messageListRef!.scrollTop = messageListRef!.scrollHeight
        setAtBottom(true)
        autoScrollPending = false
      })
      if (props.messages.length > 150) {
        props.onTrimOldMessages?.()
      }
    }
  })

  // Viewport restoration on tab switch.
  createEffect(on(
    () => props.savedViewportScroll,
    (saved) => {
      if (!messageListRef)
        return
      if (!saved) {
        requestAnimationFrame(() => checkAtBottom())
        return
      }
      requestAnimationFrame(() => {
        if (!messageListRef)
          return
        if (saved.atBottom) {
          messageListRef.scrollTop = messageListRef.scrollHeight
          setAtBottom(true)
        }
        else {
          const maxScroll = messageListRef.scrollHeight - messageListRef.clientHeight
          if (saved.distFromBottom > maxScroll) {
            messageListRef.scrollTop = 0
          }
          else {
            messageListRef.scrollTop = maxScroll - saved.distFromBottom
          }
          setAtBottom(false)
        }
        props.onClearSavedViewportScroll?.()
      })
    },
  ))

  onMount(() => {
    if (!containerRef)
      return
    onCleanup(() => {
      cancelScrollAnimation()
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
                <LoaderCircle size={iconSize.sm} class={spinner} />
                Loading older messages...
              </div>
            </Show>
            <div class={styles.messageListSpacer} />
            <For each={props.messages}>
              {msg => (
                <div data-seq={msg.seq.toString()}>
                  <MessageBubble
                    message={msg}
                    error={props.messageErrors?.[msg.id]}
                    onRetry={() => props.onRetryMessage?.(msg.id)}
                    onDelete={() => props.onDeleteMessage?.(msg.id)}
                    workingDir={props.workingDir}
                    homeDir={props.homeDir}
                  />
                </div>
              )}
            </For>
            <Show when={props.streamingText}>
              <div class={assistantMessage}>
                {/* eslint-disable-next-line solid/no-innerhtml -- streaming text rendered via remark */}
                <div class={markdownContent} innerHTML={renderedStreamHtml()} />
              </div>
            </Show>
            <Show when={props.agentWorking && !props.streamingText}>
              <ThinkingIndicator />
            </Show>
          </Show>
        </div>
        <Show when={!atBottom()}>
          <IconButton
            icon={ArrowDown}
            iconSize={iconSize.md}
            onClick={scrollToBottom}
          />
        </Show>
      </div>
    </div>
  )
}
