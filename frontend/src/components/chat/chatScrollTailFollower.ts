export interface ChatTailFollowerDeps {
  hasNewerMessages: () => boolean
  cancelAnimation: () => void
  retakeControl: () => void
  rearmBuffer: () => void
  /** Record the reader's request for the live tail. */
  followTail: () => void
  /** End the reader's request for the live tail. */
  leaveFollow: () => void
  /**
   * Whether the request for the live tail still stands: the `following` flag of the
   * scroll hook, which stays set while newer pages are on their way.
   */
  followRequested: () => boolean
  jumpToLatest: () => Promise<void> | void
  checkAtBottom: () => void
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
export function createChatTailFollower(deps: ChatTailFollowerDeps) {
  let jumpRequestRevision = 0
  // The revision of the jump whose `then` or `catch` branch did not run yet.
  let pendingJumpRevision: number | null = null

  // A request that supersedes the pending jump: the jump's branches then do nothing.
  const supersedeJump = (): number => {
    pendingJumpRevision = null
    return ++jumpRequestRevision
  }

  const forceScrollToBottom = (): void => {
    const requestRevision = supersedeJump()
    deps.cancelAnimation()
    deps.retakeControl()
    if (!deps.hasNewerMessages()) {
      deps.stickToBottom()
      return
    }

    deps.rearmBuffer()
    deps.followTail()
    pendingJumpRevision = requestRevision
    const settle = () => {
      if (pendingJumpRevision === requestRevision)
        pendingJumpRevision = null
      return requestRevision === jumpRequestRevision
    }
    void Promise.resolve(deps.jumpToLatest())
      .then(() => {
        if (!settle())
          return
        if (!deps.followRequested())
          return
        if (deps.hasNewerMessages())
          deps.checkAtBottom()
        else
          deps.stickToBottom()
      })
      .catch(() => {
        if (!settle())
          return
        if (deps.followRequested())
          deps.leaveFollow()
        deps.checkAtBottom()
      })
  }

  const jumpToBottom = (): void => {
    supersedeJump()
    deps.cancelAnimation()
    deps.stickToBottom()
  }

  const scrollToBottom = (): void => {
    if (deps.hasNewerMessages()) {
      forceScrollToBottom()
    }
    else {
      supersedeJump()
      deps.animateToBottom()
    }
  }

  return {
    forceScrollToBottom,
    jumpToBottom,
    scrollToBottom,
    /** The revision of the latest-page jump that did not settle yet, or `null`. */
    pendingJump: (): number | null => pendingJumpRevision,
  }
}

export type ChatTailFollower = ReturnType<typeof createChatTailFollower>
