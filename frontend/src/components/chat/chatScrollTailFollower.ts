import type { ScrollAnchor } from '~/stores/chatTypes'

interface AnchorState {
  anchor: ScrollAnchor
  viewportOffsetRatio: number
}

export interface ChatTailFollowerDeps {
  getEl: () => HTMLDivElement | undefined
  hasNewerMessages: () => boolean
  cancelAnimation: () => void
  retakeControl: () => void
  rearmBuffer: () => void
  currentAnchorState: () => AnchorState | null
  followTail: () => void
  atBottomSnapshot: () => boolean
  setAtBottom: (value: boolean) => void
  jumpToLatest: () => Promise<void> | void
  checkAtBottom: () => void
  stickToBottom: () => void
  setAnchor: (anchor: ScrollAnchor | null, captureTop?: number, viewportOffsetRatio?: number) => void
  anchorAtCurrentTop: () => ScrollAnchor | null
  animateToBottom: () => void
}

/**
 * Own catch-up, failure recovery, and the public ways to return to the live tail.
 *
 * A latest-page jump replaces the message window. The controller records the
 * prior anchor before it enters follow mode. A failed jump restores that anchor,
 * unless the user scrolled while the request was active. A successful jump only
 * sticks when no newer page appeared during the request.
 */
export function createChatTailFollower(deps: ChatTailFollowerDeps) {
  const forceScrollToBottom = (): void => {
    deps.cancelAnimation()
    deps.retakeControl()
    if (!deps.hasNewerMessages()) {
      deps.stickToBottom()
      return
    }

    deps.rearmBuffer()
    const previousAnchorState = deps.currentAnchorState()
    deps.followTail()
    deps.setAtBottom(true)
    void Promise.resolve(deps.jumpToLatest())
      .then(() => {
        if (!deps.atBottomSnapshot())
          return
        if (deps.hasNewerMessages())
          deps.checkAtBottom()
        else
          deps.stickToBottom()
      })
      .catch(() => {
        if (!deps.atBottomSnapshot()) {
          deps.checkAtBottom()
          return
        }
        if (previousAnchorState) {
          deps.setAnchor(
            previousAnchorState.anchor,
            undefined,
            previousAnchorState.viewportOffsetRatio,
          )
        }
        else {
          deps.setAnchor(deps.getEl() ? deps.anchorAtCurrentTop() : null)
        }
        deps.checkAtBottom()
      })
  }

  const jumpToBottom = (): void => {
    deps.cancelAnimation()
    deps.stickToBottom()
  }

  const scrollToBottom = (): void => {
    if (deps.hasNewerMessages())
      forceScrollToBottom()
    else
      deps.animateToBottom()
  }

  return { forceScrollToBottom, jumpToBottom, scrollToBottom }
}

export type ChatTailFollower = ReturnType<typeof createChatTailFollower>
