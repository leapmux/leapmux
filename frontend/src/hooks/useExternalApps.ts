import type { Accessor } from 'solid-js'
import type { ExternalApp } from '~/api/platformBridge'
import { createMemo, createResource, createSignal } from 'solid-js'
import { withChatScrollPreserved } from '~/components/chat/chatScrollPreserve'
import { usePreferences } from '~/context/PreferencesContext'
import { externalApps, launchExternalApp, loadExternalApps, resolvePreferredExternalApp } from '~/lib/externalApps'
import { compareNames } from '~/lib/fileSort'
import { createLogger } from '~/lib/logger'

const log = createLogger('external-apps')

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
 * The LIST itself is not per-instance state. It lives in one signal in
 * `~/lib/externalApps`, which every instance reads, so a refresh from any
 * surface reaches all of them. This hook adds only what is reactive per
 * consumer: when to start the probe, and how to sort and name the result.
 *
 * `enabled` gates the probe, because three of the four callers are menus: one
 * of those mounts per row, so an ungated fetch would ask the sidecar once per
 * repository in the sidebar before anybody opened anything.
 */
export function useExternalApps(enabled: Accessor<boolean>): ExternalApps {
  const prefs = usePreferences()

  // A trigger, not a store. Solid skips the fetcher entirely for a falsy
  // source, which IS the gate, and the answer goes to the shared signal rather
  // than into this resource's value.
  //
  // Caught here: Solid re-throws a rejected resource from the accessor, and the
  // accessor is read inside menu JSX, so a sidecar that cannot answer would
  // replace the whole shell with the route's error boundary instead of hiding
  // one item.
  createResource(enabled, async () => {
    try {
      await loadExternalApps()
    }
    catch (err: unknown) {
      log.warn('list_external_apps failed; offering no application', { err })
    }
    return true
  })

  // Sorted by display name, through the shared comparator so the order matches
  // every other name list in the app and does not depend on the browser's
  // locale. The KIND ordering is the menu's business, not this hook's: it
  // groups the file manager ahead of the editors and reads the kind off the
  // wire.
  const sorted = createMemo<ExternalApp[]>(() =>
    [...externalApps()].sort((a, b) => compareNames(a.displayName, b.displayName)),
  )

  // The pin may name an application that has since been uninstalled. Answer
  // undefined then, rather than silently launching a different one.
  const preferred = createMemo<ExternalApp | undefined>(() => {
    const id = prefs.preferredExternalAppId()
    if (!id)
      return undefined
    return externalApps().find(a => a.id === id)
  })

  const launch = (id: string, dir: string) => {
    if (!dir)
      return
    prefs.setPreferredExternalAppId(id)
    const name = externalApps().find(a => a.id === id)?.displayName ?? id
    launchExternalApp(id, name, dir)
  }

  const [refreshing, setRefreshing] = createSignal(false)

  const runRefresh = async () => {
    try {
      const fresh = await loadExternalApps(true)
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
    // A refresh that changes the list re-renders every open menu, and that
    // layout pass is long enough to clamp an unrelated chat tile's scrollTop
    // to 0. The repair belongs to the chat, which owns that DOM.
    await withChatScrollPreserved(async () => {
      try {
        await runRefresh()
      }
      finally {
        setRefreshing(false)
      }
    })
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
