import type { Accessor } from 'solid-js'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createMemo, untrack } from 'solid-js'
import { cannotLeaveStickyBand } from './chatScrollGeometry'

export interface ChatPaginationPolicyDeps {
  getEl: () => HTMLDivElement | undefined
  hasOlder: Accessor<boolean>
  fetchingOlder: Accessor<boolean>
  hasNewer: Accessor<boolean>
  fetchingNewer: Accessor<boolean>
  geomVersion: Accessor<unknown>
  isAtTopEdge: () => boolean
  isAtBottomEdge: () => boolean
  preserveBrowsingPosition: () => void
  clearRestoreSuppression: () => void
  onLoadOlder: () => void
  onLoadNewer: () => void
  isAtBottom: () => boolean
  isFollowing: () => boolean
  anchorAtViewportTop: () => ScrollAnchor | null
  bufferScreens: number
}

/**
 * Own pagination eligibility, explicit edge intent, stalls, and live-tail suppression.
 *
 * A stall is stricter than a background prefetch. It requires an active fetch,
 * more history, and a viewport clamped at the loaded edge. Live-tail suppression
 * applies only after the pane can leave the sticky band. A short hidden-heavy page
 * must still fetch older rows until it becomes scrollable.
 */
export function createChatPaginationPolicy(deps: ChatPaginationPolicyDeps) {
  const bufferTargetPx = (): number => {
    const element = deps.getEl()
    return element ? deps.bufferScreens * element.clientHeight : 0
  }

  const canLoadOlderMessages = (): boolean =>
    deps.getEl() !== undefined && deps.hasOlder() && !deps.fetchingOlder()

  const loadOlderMessages = (): boolean => {
    if (!canLoadOlderMessages())
      return false
    // A prepend changes geometry above the viewport. Preserve the anchor before
    // the request starts so the re-pin absorbs that growth.
    deps.preserveBrowsingPosition()
    deps.onLoadOlder()
    return true
  }

  const tryLoadOlderOnExplicitTopIntent = (): boolean => {
    if (!deps.isAtTopEdge() || !canLoadOlderMessages())
      return false
    deps.clearRestoreSuppression()
    return loadOlderMessages()
  }

  const canLoadNewerMessages = (): boolean =>
    deps.getEl() !== undefined && deps.hasNewer() && !deps.fetchingNewer()

  const loadNewerMessages = (): boolean => {
    if (!canLoadNewerMessages())
      return false
    deps.onLoadNewer()
    return true
  }

  const tryLoadNewerOnExplicitBottomIntent = (): boolean => {
    if (!deps.isAtBottomEdge() || !canLoadNewerMessages())
      return false
    return loadNewerMessages()
  }

  const stalledOlder = createMemo(() => {
    deps.geomVersion()
    return deps.fetchingOlder() && deps.hasOlder() && deps.isAtTopEdge()
  })
  const stalledNewer = createMemo(() => {
    deps.geomVersion()
    return deps.fetchingNewer() && deps.hasNewer() && deps.isAtBottomEdge()
  })

  const suppressOlderPrefetchAtLiveTail = (): boolean => {
    const element = deps.getEl()
    if (!element || cannotLeaveStickyBand(element))
      return false
    if (!deps.hasNewer() && deps.isAtBottom())
      return true
    return deps.isFollowing() && untrack(deps.anchorAtViewportTop) !== null
  }

  return {
    bufferTargetPx,
    canLoadOlderMessages,
    loadOlderMessages,
    tryLoadOlderOnExplicitTopIntent,
    canLoadNewerMessages,
    loadNewerMessages,
    tryLoadNewerOnExplicitBottomIntent,
    stalledOlder,
    stalledNewer,
    suppressOlderPrefetchAtLiveTail,
  }
}

export type ChatPaginationPolicy = ReturnType<typeof createChatPaginationPolicy>
