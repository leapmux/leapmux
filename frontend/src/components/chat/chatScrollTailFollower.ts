import type { ScrollContext } from './useChatScroll'
import { batch, createSignal } from 'solid-js'

/** The deps that only createChatTailFollower reads, beyond the shared ScrollContext. */
export interface ChatTailFollowerExtras {
  /**
   * Replace the message window with the latest page. `signal` is the signal of the
   * pending history jump of the hook: an abort abandons the fetch.
   */
  jumpToLatest: (signal: AbortSignal) => Promise<boolean> | undefined
  animateToBottom: () => void
}

/**
 * Own catch-up, failure recovery, and the public ways to return to the live tail.
 *
 * A latest-page jump replaces the message window. The controller records the request
 * for the tail before the jump. A move of the reader during the request ends it, and a
 * jump that settles after that leaves the view where the reader went. A successful jump
 * asks for the tail only when no newer page appeared during the request. A failed jump
 * ends the request, so no later restore or resize moves the view for it.
 *
 * The rows of the tail can be staged: loaded, and not measured yet (see chatRowAdmission).
 * So each command asks the hook for the tail (ScrollContext.requestPlace), and the view
 * moves once, when the last rows are measured. With no staged row, the place is shown at
 * once.
 *
 * Each command runs its synchronous part in one batch, and so does each branch of the
 * landing of a jump: the first-view rule of the hook reads the pending jump and the reader
 * place, and it must not run between two steps of one command and ask for a place of its
 * own.
 *
 * The fetch of the jump is the pending history jump of the hook (see
 * ScrollContext.beginHistoryJump). A later command, or an input by which the reader takes
 * control, aborts it, and the landing then does nothing. The landing reads no result of
 * the fetch: a window with no newer page holds the tail that the reader asked for,
 * whichever fetch of the store delivered it.
 */
export function createChatTailFollower(ctx: ScrollContext, extras: ChatTailFollowerExtras) {
  let jumpRequestRevision = 0
  // Whether the jump of the current revision did not run its `then` or `catch` branch yet.
  // A jump of an older revision is never pending: the request that superseded it ended it.
  // The first-view rule of the hook does not read this signal: it reads the pending history
  // jump of the hook, and each reader of this value reads it untracked.
  const [jumpPending, setJumpPending] = createSignal(false)
  // The signal of the jump of the current revision, while that jump is pending.
  let jumpSignal: AbortSignal | undefined

  // A request that supersedes the pending jump: the jump's branches then do nothing.
  const supersedeJump = (): number => {
    setJumpPending(false)
    jumpSignal = undefined
    return ++jumpRequestRevision
  }

  /** Ask the hook for the tail: its stick runs when the last rows are measured. */
  const requestTail = (): void => {
    void ctx.requestPlace({ kind: 'tail' })
  }

  const forceScrollToBottom = (): void => batch(() => {
    const requestRevision = supersedeJump()
    // This command ends each history jump before it, also when it starts no fetch of its
    // own: the scroll-to-bottom button with no newer page ends the fetch of Home. The jump
    // of this module no longer counts as pending here, so the call keeps the request for
    // the tail.
    ctx.abandonHistoryJump()
    ctx.cancelScrollAnimation()
    ctx.retakeScrollControl()
    if (!ctx.hasNewerMessages()) {
      requestTail()
      return
    }

    // Before this jump counts as pending, and before the request for the tail:
    // beginHistoryJump ends the request of a jump to the latest page that is still pending,
    // and this jump must not count as one.
    const signal = ctx.beginHistoryJump()
    // The reader place becomes the tail. Follow mode does not apply until the jump settled
    // (see ScrollContext.isFollowing).
    ctx.followTail()
    setJumpPending(true)
    jumpSignal = signal
    // Whether this jump is still the current request, and nothing aborted it. A current
    // jump then stops being pending, here and in the hook.
    const settle = () => {
      const current = requestRevision === jumpRequestRevision
      if (current) {
        setJumpPending(false)
        jumpSignal = undefined
      }
      return ctx.settleHistoryJump(signal) && current
    }
    void Promise.resolve(extras.jumpToLatest(signal))
      .then(() => batch(() => {
        if (!settle())
          return
        if (!ctx.followRequested())
          return
        if (ctx.hasNewerMessages())
          ctx.checkPosition()
        else
          requestTail()
      }))
      .catch(() => batch(() => {
        if (!settle())
          return
        if (ctx.followRequested())
          ctx.leaveFollow()
        ctx.checkPosition()
      }))
  })

  const scrollToBottom = (): void => {
    // The animation moves inside the rows of the list. With rows staged below, or with no
    // row in the list, the bottom of the list is not the tail, so the command asks for the
    // tail as End does.
    if (ctx.hasNewerMessages() || ctx.stagedNewer()) {
      forceScrollToBottom()
      return
    }
    batch(() => {
      supersedeJump()
      // The button starts no fetch here, but it still ends one that runs.
      ctx.abandonHistoryJump()
      // A command of the reader, as End is: it ends the pause of the buffer fill that a
      // forced stop started. Its `scroll` events are own writes, which never end it.
      ctx.retakeScrollControl()
      extras.animateToBottom()
    })
  }

  return {
    forceScrollToBottom,
    scrollToBottom,
    /**
     * The revision of the latest-page jump that did not settle yet, or `null`. A jump
     * whose signal is aborted is not pending: its landing does nothing. Reactive in the
     * settled state of the jump, and not in the abort of its signal.
     */
    pendingJump: (): number | null => jumpPending() && jumpSignal?.aborted !== true
      ? jumpRequestRevision
      : null,
  }
}

export type ChatTailFollower = ReturnType<typeof createChatTailFollower>
