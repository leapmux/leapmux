import type { ScrollContext } from './useChatScroll'

/** The deps that only createChatTailFollower reads, beyond the shared ScrollContext. */
export interface ChatTailFollowerExtras {
  /** Replace the message window with the latest page. */
  jumpToLatest: () => Promise<void> | void
  stickToBottom: () => void
  animateToBottom: () => void
}

/**
 * Own catch-up, failure recovery, and the public ways to return to the live tail.
 *
 * A latest-page jump replaces the message window. The controller records the request
 * for the tail before the jump. A move of the reader during the request ends it, and a
 * jump that settles after that leaves the view where the reader went. A successful jump
 * sticks only when no newer page appeared during the request. A failed jump ends the
 * request, so no later restore or resize moves the view for it.
 */
export function createChatTailFollower(ctx: ScrollContext, extras: ChatTailFollowerExtras) {
  let jumpRequestRevision = 0
  // Whether the jump of the current revision did not run its `then` or `catch` branch yet.
  // A jump of an older revision is never pending: the request that superseded it ended it.
  let jumpPending = false

  // A request that supersedes the pending jump: the jump's branches then do nothing.
  const supersedeJump = (): number => {
    jumpPending = false
    return ++jumpRequestRevision
  }

  const forceScrollToBottom = (): void => {
    const requestRevision = supersedeJump()
    ctx.cancelScrollAnimation()
    ctx.retakeScrollControl()
    if (!ctx.hasNewerMessages()) {
      extras.stickToBottom()
      return
    }

    ctx.rearmScrollBufferFiller()
    ctx.followTail()
    jumpPending = true
    // Whether this jump is still the current request; it then stops being pending.
    const settle = () => {
      const current = requestRevision === jumpRequestRevision
      if (current)
        jumpPending = false
      return current
    }
    void Promise.resolve(extras.jumpToLatest())
      .then(() => {
        if (!settle())
          return
        if (!ctx.followRequested())
          return
        if (ctx.hasNewerMessages())
          ctx.checkPosition()
        else
          extras.stickToBottom()
      })
      .catch(() => {
        if (!settle())
          return
        if (ctx.followRequested())
          ctx.leaveFollow()
        ctx.checkPosition()
      })
  }

  const scrollToBottom = (): void => {
    if (ctx.hasNewerMessages()) {
      forceScrollToBottom()
    }
    else {
      supersedeJump()
      extras.animateToBottom()
    }
  }

  return {
    forceScrollToBottom,
    scrollToBottom,
    /** The revision of the latest-page jump that did not settle yet, or `null`. */
    pendingJump: (): number | null => jumpPending
      ? jumpRequestRevision
      : null,
  }
}

export type ChatTailFollower = ReturnType<typeof createChatTailFollower>
