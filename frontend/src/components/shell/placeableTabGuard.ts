import type { createLayoutStore } from '~/stores/layout.store'
import { showWarnToast } from '~/components/common/Toast'
import { hasPlaceableTab } from './openTabInFocusedTile'

/**
 * The shared pre-RPC refusal for every path that creates a worker-side
 * resource and then places a tab: warn and answer false when no tile can
 * take the tab, so the caller never creates an orphan. `what` names the
 * resource in the toast title ("an agent", "a terminal", "the file").
 *
 * Hooks surface the refusal as this toast; the creation dialogs surface
 * it as a disabled submit with the reason visible (`newTabBlockedReason`
 * in AppShellDialogs), because their submit never reaches the RPC.
 */
export function warnUnlessPlaceableTab(
  layoutStore: ReturnType<typeof createLayoutStore>,
  what: string,
): boolean {
  if (hasPlaceableTab(layoutStore))
    return true
  showWarnToast(`Cannot open ${what}`, new Error('The workspace is not ready for a new tab yet.'))
  return false
}
