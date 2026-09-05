import type { Accessor } from 'solid-js'
import type { ExternalApp } from '~/api/platformBridge'
import { createMemo, createResource, createSignal, getOwner, runWithOwner } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { showWarnToast } from '~/components/common/Toast'
import { usePreferences } from '~/context/PreferencesContext'
import { loadExternalApps, resolvePreferredExternalApp } from '~/lib/externalApps'
import { createLogger } from '~/lib/logger'

const log = createLogger('external-apps')

// Hoisted: Intl.Collator construction is non-trivial; reusing one across
// every refresh keeps the sort cheap.
const appCollator = new Intl.Collator(undefined, { sensitivity: 'base' })

// Selector used by the post-refresh scroll-restore guard to find active
// chat scroll containers in the DOM.
const CHAT_SCROLL_CONTAINER_SELECTOR = '[data-chat-scroll-container="true"]'

// Skip the restore if the chat's scrollHeight changed by more than this
// many pixels — that signals a real content reload, not the spurious
// clamp we're trying to undo.
const CHAT_SCROLL_RESTORE_HEIGHT_TOLERANCE_PX = 200

export interface ExternalApps {
  /** Every detected application, sorted for display. Empty until enabled. */
  apps: Accessor<ExternalApp[]>
  /**
   * The remembered application, or undefined when the pin names one that is
   * no longer detected. Naming it must not persist anything, so this is the
   * read-only pick.
   */
  preferred: Accessor<ExternalApp | undefined>
  /** The stored pin itself, for the check mark in a menu. */
  preferredId: Accessor<string | undefined>
  /** Open `dir` in the application, remember it, and report a failure. */
  launch: (id: string, dir: string) => void
  /** Re-probe the machine. Resolves once the new list is in place. */
  refresh: () => Promise<void>
  refreshing: Accessor<boolean>
}

/**
 * The detected external applications, and the one way to launch one.
 *
 * Every surface that offers "Open in ..." shares this: the title bar's split
 * button, the workspace row menu, the branch row menu and the repository row
 * menu. Before it, each of the four carried its own `createResource`, its own
 * swallow of a sidecar that cannot answer, and its own `catch` that logged the
 * failure where no user could see it.
 *
 * `enabled` gates the probe, because three of the four callers are menus: one
 * of those mounts per row, so an ungated fetch would ask the sidecar once per
 * repository in the sidebar before anybody opened anything.
 */
export function useExternalApps(enabled: Accessor<boolean>): ExternalApps {
  const prefs = usePreferences()
  // Captured at setup: `refresh` re-enters the resource after an await, well
  // outside any reactive tracking context. Without the original owner every
  // refetch leaks the computations it creates, and the leaks accumulate until
  // one refresh blocks the main thread long enough to clamp the chat view's
  // scrollTop to 0.
  const owner = getOwner()

  const [apps, { refetch }] = createResource<ExternalApp[], boolean>(
    enabled,
    async (canRun) => {
      if (!canRun)
        return []
      // Caught here: `initialValue` does NOT suppress a rejection, and Solid
      // re-throws one from the accessor. The accessor is read inside menu JSX,
      // so a sidecar that cannot answer would replace the whole shell with the
      // route's error boundary instead of hiding one item.
      try {
        return await loadExternalApps()
      }
      catch (err: unknown) {
        log.warn('list_external_apps failed; offering no application', { err })
        return []
      }
    },
    { initialValue: [] },
  )

  // Sorted by display name, with a collator so accented names file correctly.
  // The KIND ordering is the menu's business, not this hook's: it groups the
  // file manager ahead of the editors and reads the kind off the wire.
  const sorted = createMemo<ExternalApp[]>(() => {
    const list = apps().slice()
    list.sort((a, b) => appCollator.compare(a.displayName, b.displayName))
    return list
  })

  // The pin may name an application that has since been uninstalled. Answer
  // undefined then, rather than silently launching a different one.
  const preferred = createMemo<ExternalApp | undefined>(() => {
    const id = prefs.preferredExternalAppId()
    if (!id)
      return undefined
    return apps().find(a => a.id === id)
  })

  const launch = (id: string, dir: string) => {
    if (!dir)
      return
    prefs.setPreferredExternalAppId(id)
    const name = apps().find(a => a.id === id)?.displayName ?? id
    platformBridge.openInExternalApp(id, dir).catch((err: unknown) => {
      // Surfaced, not only logged. A launch that fails looks exactly like an
      // application opening behind this window, so staying silent left the
      // user with no way to tell the two apart.
      showWarnToast(`Could not open ${name}`, err)
    })
  }

  const [refreshing, setRefreshing] = createSignal(false)

  const runRefresh = async () => {
    try {
      const fresh = await loadExternalApps(true)
      // Only emit a new apps() signal if the set actually changed. The common
      // case (the user hits refresh just to check) returns an identical list —
      // skipping the refetch avoids re-running every menu's <For> and the
      // layout pass that comes with it.
      const current = apps()
      const sameList = fresh.length === current.length
        && fresh.every((a, i) => a.id === current[i]?.id && a.displayName === current[i]?.displayName)
      if (!sameList) {
        if (owner)
          await runWithOwner(owner, () => refetch())
        else
          await refetch()
      }
      // If the pin points at an application that is no longer detected, fall
      // back to the first remaining one. `resolvePreferredExternalApp`
      // persists through the reactive setter, so the keyboard shortcut and
      // every menu agree on which application "default launch" picks.
      //
      // An EMPTY list changes nothing. Detection can come back empty for a
      // reason that is not "the user uninstalled it" — a transient probe
      // failure is enough — and clearing the pin then would throw away a
      // choice that must return when the application does.
      const pinned = prefs.preferredExternalAppId()
      if (pinned && fresh.length > 0 && !fresh.some(a => a.id === pinned))
        resolvePreferredExternalApp(fresh, pinned, prefs.setPreferredExternalAppId)
    }
    catch (err) {
      log.warn('refresh applications failed', err)
    }
  }

  const refresh = async () => {
    if (refreshing())
      return
    setRefreshing(true)

    // When the list actually changes, the refetch → Solid flush → DOM diff →
    // browser layout pass takes long enough (~70ms) to block rAF, and during
    // that gap the active chat tile's scrollTop gets reset to 0 by some
    // browser-internal mechanism we could not pinpoint despite extensive
    // instrumentation. Snapshot scrollTop on every chat container before the
    // refresh and restore it after the layout pass settles.
    //
    // Iterate over all chat containers (the visible tab plus any hidden ones)
    // so each preserves its own state — querySelector alone would only catch
    // the first DOM match, which is often the hidden tab.
    //
    // It lives here rather than beside the title bar's button, where it was
    // written: the same refresh now runs from three row menus, and every one
    // of them re-lays out the same chat containers.
    const snapshots = Array.from(
      document.querySelectorAll<HTMLDivElement>(CHAT_SCROLL_CONTAINER_SELECTOR),
    ).map(el => ({ el, scrollTop: el.scrollTop, scrollHeight: el.scrollHeight }))

    try {
      await runRefresh()
    }
    finally {
      setRefreshing(false)
      // Run after Solid's flush + the browser's layout pass have settled.
      requestAnimationFrame(() => requestAnimationFrame(() => {
        for (const s of snapshots) {
          if (!s.el.isConnected)
            continue
          if (s.el.scrollTop === s.scrollTop)
            continue
          const heightDelta = Math.abs(s.el.scrollHeight - s.scrollHeight)
          if (heightDelta < CHAT_SCROLL_RESTORE_HEIGHT_TOLERANCE_PX)
            s.el.scrollTop = s.scrollTop
        }
      }))
    }
  }

  return {
    apps: sorted,
    preferred,
    preferredId: prefs.preferredExternalAppId,
    launch,
    refresh,
    refreshing,
  }
}
