import { createAccountScopedSignal } from '~/lib/accountScopedSignal'
import { KEY_EXPANDED_WORKSPACES, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'

/**
 * Which workspace rows have their tab tree expanded, for the WHOLE app.
 *
 * One module-scope signal, because the set is one document under one key and
 * several components read it at once. `sections/defaults.go` seeds In progress
 * AND Archived into the left sidebar, and `CollapsibleSidebar` calls
 * `content()` for every expandable section -- so one sidebar mounts one
 * `WorkspaceSectionContent` per workspace section, and a custom workspace
 * section can now live on the right sidebar as well.
 *
 * Each instance used to hold its own `createSignal(readExpandedWorkspaceIds())`
 * and write the WHOLE set back with no merge, so expanding a row in one
 * instance stored a set that omitted every row the others had expanded. There
 * is no divergence at mount -- every instance reads the same value -- so the
 * bug only appears after the first toggle.
 *
 * The lifecycle (lazy first read, quiet persist, re-read on an account switch)
 * belongs to `~/lib/accountScopedSignal`, which `workspaceListState` shares.
 */

const EMPTY: ReadonlySet<string> = new Set()

const expanded = createAccountScopedSignal<ReadonlySet<string>>(EMPTY, {
  read: () => {
    const stored = sessionStorageGet<string[]>(KEY_EXPANDED_WORKSPACES)
    return stored ? new Set(stored) : EMPTY
  },
  write: current => sessionStorageSet(KEY_EXPANDED_WORKSPACES, [...current]),
})

/** Whether `workspaceId`'s tab tree is expanded. Reactive. */
export function isWorkspaceExpanded(workspaceId: string): boolean {
  return expanded.get().has(workspaceId)
}

/** Flip `workspaceId` between expanded and collapsed. */
export function toggleWorkspaceExpanded(workspaceId: string): void {
  expanded.set((prev) => {
    const next = new Set(prev)
    if (next.has(workspaceId))
      next.delete(workspaceId)
    else
      next.add(workspaceId)
    return next
  })
}

/**
 * Expand or collapse every id in `workspaceIds`, leaving every other id alone.
 *
 * A SET operation rather than a replace, because the caller is one section's
 * "Collapse all" / "Expand all" and the other sections' rows are not its to
 * touch. That is exactly the merge the per-instance signals could not do.
 */
export function setWorkspacesExpanded(workspaceIds: readonly string[], expandedState: boolean): void {
  expanded.set((prev) => {
    const next = new Set(prev)
    let changed = false
    for (const id of workspaceIds) {
      if (expandedState === next.has(id))
        continue
      changed = true
      if (expandedState)
        next.add(id)
      else
        next.delete(id)
    }
    // Identity-stable on a no-op, so a "Collapse all" on an already-collapsed
    // section notifies nothing and writes nothing.
    return changed ? next : prev
  })
}

/**
 * Drop the in-memory mirror. FOR TESTS ONLY.
 *
 * A suite that swaps the storage account, or that seeds sessionStorage after
 * this module already read it, needs the next access to read again.
 */
export function resetExpandedWorkspacesForTests(): void {
  expanded.reset()
}
