import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import { batch, createEffect, untrack } from 'solid-js'
import { captureTerminalScreens } from '~/components/terminal/TerminalView'

/**
 * Snapshot the OUTGOING workspace's tab + layout state into the
 * registry the moment the URL workspace id changes — BEFORE the
 * activeWorkspaceId signal flips, so downstream effects (CRDT
 * reconciler, useWorkspaceRestore's `tabStore.clear()`) can't clobber
 * the previous workspace's tabs in the registry cache. Floating-window
 * state is omitted: `floatingWindowStore` is projection-driven and
 * re-derives from the CRDT bridge on workspace re-activation.
 *
 * The user's cached-restore flow round-trips through
 * `registry.get(workspaceId)` later; if the snapshot here is missing
 * or stale, the next workspace-switch-back lands on an empty tile.
 *
 * Solid's effect-scheduling order isn't strictly "earlier-registered
 * first" in practice — useWorkspaceRestore's createEffect was firing
 * first and calling tabStore.clear() before the snapshot effect could
 * read the outgoing workspace's tabs. Snapshotting BEFORE setting the
 * activeWorkspaceId signal inside the same `batch` is the reliable fix.
 */
export interface UseWorkspaceSwitchSnapshotOpts {
  getURLWorkspaceId: () => string | null | undefined
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  registry: ReturnType<typeof createWorkspaceStoreRegistry>
  setActiveWorkspaceId: (next: string | null) => void
}

export function useWorkspaceSwitchSnapshot(opts: UseWorkspaceSwitchSnapshotOpts): void {
  let snapshotOutgoingWorkspaceId: string | null = null
  createEffect(() => {
    const next = opts.getURLWorkspaceId() ?? null
    batch(() => {
      if (snapshotOutgoingWorkspaceId && next && snapshotOutgoingWorkspaceId !== next) {
        untrack(() => {
          // Refresh each TERMINAL tab's `screen` from its live xterm
          // buffer before snapshotting. The tab store only carries the
          // initial bytes from the first `ListTerminals` call; without
          // this pass, any output the user has been watching since then
          // would be lost the moment the outgoing TerminalView unmounts
          // and disposes the xterm instance — and the next switch-back
          // would only replay the original initial snapshot.
          const baseSnapshot = opts.tabStore.snapshot()
          opts.registry.set(snapshotOutgoingWorkspaceId!, {
            ...baseSnapshot,
            tabs: captureTerminalScreens(baseSnapshot.tabs),
            workspaceId: snapshotOutgoingWorkspaceId!,
            layout: opts.layoutStore.snapshot(),
            restored: true,
            tabsLoaded: true,
          })
        })
      }
      if (next)
        snapshotOutgoingWorkspaceId = next
      opts.setActiveWorkspaceId(next)
    })
  })
}
