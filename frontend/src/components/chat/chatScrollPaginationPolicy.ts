import type { Accessor } from 'solid-js'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createMemo, untrack } from 'solid-js'
import { cannotScroll } from './chatScrollGeometry'

export interface ChatPaginationPolicyDeps {
  getEl: () => HTMLDivElement | undefined
  hasOlder: Accessor<boolean>
  fetchingOlder: Accessor<boolean>
  hasNewer: Accessor<boolean>
  fetchingNewer: Accessor<boolean>
  geomVersion: Accessor<unknown>
  isAtTopEdge: () => boolean
  isAtBottomEdge: () => boolean
  onLoadOlder: () => void
  onLoadNewer: () => void
  isAtBottom: () => boolean
  /**
   * Follow mode (see ScrollContext.isFollowing). Reactive: the hook passes the accessor
   * that reads the reader place and the pending history jump tracked, so a memo that reads
   * it runs again when follow mode starts or ends. A function that an effect calls reads
   * it untracked (see suppressOlderPrefetchAtLiveTail).
   */
  isFollowing: Accessor<boolean>
  anchorAtViewportTop: () => ScrollAnchor | null
  bufferScreens: number
  /**
   * Loaded rows wait above the rows of the list for their measure (staged rows, see
   * chatRowAdmission), or no row is in the list yet. Reactive.
   */
  stagedOlder: Accessor<boolean>
  /** The same for the rows below the rows of the list. Reactive. */
  stagedNewer: Accessor<boolean>
  /** Loaded rows exist and none is in the list yet. Reactive. */
  awaitingFirstView: Accessor<boolean>
}

/**
 * Own pagination eligibility, explicit edge intent, stalls, and live-tail suppression.
 *
 * A stall is stricter than a background prefetch. It requires an active fetch,
 * more history, and a viewport clamped at the loaded edge. Live-tail suppression
 * applies only while the pane can scroll. A short hidden-heavy page must still fetch
 * older rows until it becomes scrollable.
 *
 * No side loads a page while rows are staged on it: staged rows are loaded content that
 * has no pixels yet, so the edge of the list is not the edge of the loaded rows. Without
 * this rule, a reader at the edge of the measured rows would fetch older pages up to the
 * window ceiling while the hidden copies still measure the first page.
 */
export function createChatPaginationPolicy(deps: ChatPaginationPolicyDeps) {
  const bufferTargetPx = (): number => {
    const element = deps.getEl()
    return element
      ? deps.bufferScreens * element.clientHeight
      : 0
  }

  const loadOlderMessages = (): boolean => {
    if (deps.getEl() === undefined || !deps.hasOlder() || deps.fetchingOlder() || deps.stagedOlder())
      return false
    // A prepend changes geometry above the viewport. The layout shift keeps the view in
    // place across it, so nothing needs to be held for it here.
    deps.onLoadOlder()
    return true
  }

  const tryLoadOlderOnExplicitTopIntent = (): boolean =>
    deps.isAtTopEdge() && loadOlderMessages()

  const loadNewerMessages = (): boolean => {
    if (deps.getEl() === undefined || !deps.hasNewer() || deps.fetchingNewer() || deps.stagedNewer())
      return false
    deps.onLoadNewer()
    return true
  }

  const tryLoadNewerOnExplicitBottomIntent = (): boolean =>
    deps.isAtBottomEdge() && loadNewerMessages()

  const stalledOlder = createMemo(() => {
    deps.geomVersion()
    return deps.fetchingOlder() && deps.hasOlder() && deps.isAtTopEdge()
  })
  const stalledNewer = createMemo(() => {
    deps.geomVersion()
    return deps.fetchingNewer() && deps.hasNewer() && deps.isAtBottomEdge()
  })
  // The reader waits at an edge of the list for staged rows: the view is at that edge, and
  // loaded rows beyond it are not measured yet. Both are false while no row is in the list:
  // the first view has an indicator of its own. A follower does not wait at the bottom: each
  // live message is staged for a frame or more, and the view follows when it joins.
  const atStagedTopEdge = createMemo(() => {
    deps.geomVersion()
    return !deps.awaitingFirstView() && deps.stagedOlder() && deps.isAtTopEdge()
  })
  const atStagedBottomEdge = createMemo(() => {
    deps.geomVersion()
    return !deps.awaitingFirstView() && deps.stagedNewer() && !deps.isFollowing() && deps.isAtBottomEdge()
  })

  const suppressOlderPrefetchAtLiveTail = (): boolean => {
    const element = deps.getEl()
    if (!element || cannotScroll(element))
      return false
    if (!deps.hasNewer() && deps.isAtBottom())
      return true
    // Untracked: the effect of the buffer filler calls this function, and a change of
    // follow mode must not run a fill. The filler fills for a move of the view.
    return untrack(deps.isFollowing) && untrack(deps.anchorAtViewportTop) !== null
  }

  return {
    bufferTargetPx,
    loadOlderMessages,
    tryLoadOlderOnExplicitTopIntent,
    loadNewerMessages,
    tryLoadNewerOnExplicitBottomIntent,
    stalledOlder,
    stalledNewer,
    atStagedTopEdge,
    atStagedBottomEdge,
    suppressOlderPrefetchAtLiveTail,
  }
}
