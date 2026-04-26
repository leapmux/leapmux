import type { Accessor } from 'solid-js'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createEffect, createSignal, on, onCleanup, onMount, untrack } from 'solid-js'
import { MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'

export interface ChatScrollState {
  distFromBottom: number
  atBottom: boolean
}

export interface UseChatScrollOptions {
  messages: Accessor<AgentChatMessage[]>
  messageVersion?: Accessor<number | undefined>
  streamingText: Accessor<string>
  agentWorking?: Accessor<boolean | undefined>
  hasOlderMessages?: Accessor<boolean | undefined>
  fetchingOlder?: Accessor<boolean | undefined>
  onLoadOlderMessages?: () => void
  onTrimOldMessages?: () => void
  savedViewportScroll?: Accessor<ChatScrollState | undefined>
  onClearSavedViewportScroll?: () => void
  /** Optional ref callbacks the parent uses to expose scroll state externally. */
  registerScrollState?: (fn: () => ChatScrollState | undefined) => void
  registerScrollToBottom?: (fn: () => void) => void
  registerPageScroll?: (fn: (direction: -1 | 1) => void) => void
}

export interface UseChatScrollResult {
  atBottom: Accessor<boolean>
  /** Fresh DOM-measured atBottom check (read this when the signal might be stale). */
  isAtBottomFresh: () => boolean
  attachListRef: (el: HTMLDivElement | undefined) => void
  attachContentRef: (el: HTMLDivElement | undefined) => void
  /** Animated scroll-to-bottom (for the floating "scroll to bottom" button). */
  scrollToBottomAnimated: () => void
  /** Synchronous jump to bottom (used by the inline "show more" button). */
  jumpToBottom: () => void
  handlers: {
    onScroll: () => void
    onWheel: (event: WheelEvent) => void
    onKeyDown: (event: KeyboardEvent) => void
    onTouchStart: (event: TouchEvent) => void
    onTouchMove: (event: TouchEvent) => void
    onTouchEnd: () => void
    onTouchCancel: () => void
    onPointerDown: (event: PointerEvent) => void
    onPointerMove: (event: PointerEvent) => void
    onPointerUp: () => void
    onPointerCancel: () => void
  }
}

/**
 * Manages all scroll behavior for the chat message list:
 * sticky-bottom tracking, auto-scroll on new content, scroll anchoring when
 * older messages are prepended, viewport restoration on tab switch, and
 * load-older-on-overscroll for touch/wheel/keyboard.
 *
 * Attaches via ref callbacks so the same refs feed event handlers, effects,
 * and the externally-exposed scroll-state functions registered via the
 * optional `register*` callbacks.
 */
export function useChatScroll(opts: UseChatScrollOptions): UseChatScrollResult {
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
    !!messageListRef && !!opts.hasOlderMessages?.() && !opts.fetchingOlder?.()

  const loadOlderMessages = () => {
    if (!canLoadOlderMessages() || !isNearTop())
      return false
    setPreserveBrowsingPosition(true)
    opts.onLoadOlderMessages?.()
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
    const msgs = opts.messages()
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
  const getScrollState = (): ChatScrollState | undefined => {
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
    opts.registerScrollState?.(getScrollState)
    opts.registerScrollToBottom?.(forceScrollToBottom)
    opts.registerPageScroll?.(pageScroll)
  })

  let autoScrollFirstSeq: bigint | undefined

  // Auto-scroll the message list to the bottom when new content arrives
  // and the user is already at (or near) the bottom.
  // messageVersion covers thread merges (tool_use_result merged into an
  // existing tool_use) which don't change messages.length.
  createEffect(() => {
    const msgs = opts.messages()
    const firstSeq = msgs[0]?.seq
    const prependedOlderMessages = autoScrollFirstSeq !== undefined
      && firstSeq !== undefined
      && firstSeq < autoScrollFirstSeq
    autoScrollFirstSeq = firstSeq
    void msgs.length
    void opts.messageVersion?.()
    void opts.streamingText()
    void opts.agentWorking?.()
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
      if (msgs.length > MAX_LOADED_CHAT_MESSAGES) {
        opts.onTrimOldMessages?.()
      }
    }
  })

  // Re-check atBottom after the parent clears saved scroll state.
  // Restoration itself is handled exclusively by the ResizeObserver's
  // hidden→visible path so that we avoid a race where this effect runs
  // before the tab is actually hidden (clearing saved state too early).
  createEffect(on(
    () => opts.savedViewportScroll?.(),
    (saved) => {
      if (!saved && messageListRef && messageListRef.clientHeight > 0) {
        requestAnimationFrame(() => checkAtBottom())
      }
    },
  ))

  onCleanup(() => {
    cancelScrollAnimation()
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
      const savedScroll = wasHidden ? opts.savedViewportScroll?.() : undefined
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
            opts.onClearSavedViewportScroll?.()
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

  const scrollToBottomAnimated = () => {
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

  const jumpToBottom = () => {
    if (!messageListRef)
      return
    messageListRef.scrollTop = messageListRef.scrollHeight
  }

  return {
    atBottom,
    isAtBottomFresh: isAtBottom,
    attachListRef: (el) => {
      messageListRef = el
    },
    attachContentRef: (el) => {
      contentRef = el
    },
    scrollToBottomAnimated,
    jumpToBottom,
    handlers: {
      onScroll: handleScroll,
      onWheel: handleWheel,
      onKeyDown: handleKeyDown,
      onTouchStart: handleTouchStart,
      onTouchMove: handleTouchMove,
      onTouchEnd: clearTouchOverscroll,
      onTouchCancel: clearTouchOverscroll,
      onPointerDown: handlePointerDown,
      onPointerMove: handlePointerMove,
      onPointerUp: clearPointerOverscroll,
      onPointerCancel: clearPointerOverscroll,
    },
  }
}
