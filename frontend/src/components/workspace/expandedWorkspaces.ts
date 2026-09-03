import { createEffect, createRoot, createSignal } from 'solid-js'
import {
  hasStorageAccount,
  KEY_EXPANDED_WORKSPACES,
  onStorageAccountChange,
  sessionStorageGet,
  sessionStorageSet,
} from '~/lib/browserStorage'

/**
 * Which workspace rows have their tab tree expanded, for the WHOLE app.
 *
 * One module-scope signal, because the set is one document under one key and
 * several components read it at once. `sections/defaults.go` seeds In progress
 * AND Archived into the left sidebar, and `CollapsibleSidebar` calls
 * `content()` for every expandable section -- so one sidebar already mounts two
 * `WorkspaceSectionContent` instances. The app then mounts the sidebar twice
 * (the desktop pane and the mobile overlay), which doubles it again.
 *
 * Each instance used to hold its own `createSignal(readExpandedWorkspaceIds())`
 * and write the WHOLE set back with no merge, so expanding a row in one
 * instance stored a set that omitted every row the others had expanded. There
 * is no divergence at mount -- every instance reads the same value -- so the
 * bug only appears after the first toggle.
 *
 * The mirror belongs to the account it was read for, so it re-reads when the
 * namespace moves.
 */

const EMPTY: ReadonlySet<string> = new Set()

/**
 * Whether sessionStorage was read for the CURRENT account yet.
 *
 * The read is lazy rather than done at import time: this module is imported
 * when the bundle loads, and `AuthContext` calls `setStorageAccount` only once
 * the identity resolves. An account-scoped read before that throws.
 *
 * Declared ahead of the `createRoot` below, not beside `ensureSeeded`: Solid
 * flushes the root's effects at the end of the `createRoot` call, so the
 * persist effect reads this binding while the module body is still running.
 */
let seeded = false

function readStoredIds(): ReadonlySet<string> {
  const stored = sessionStorageGet<string[]>(KEY_EXPANDED_WORKSPACES)
  return stored ? new Set(stored) : EMPTY
}

// `createRoot`, because module scope has no owner: the effect below would
// otherwise be created outside any reactive root, never dispose, and log
// Solid's "computations created outside a `createRoot`" warning. The root is
// never disposed on purpose -- it lives as long as the module does.
const { expandedIds, setExpandedIds } = createRoot(() => {
  const [ids, setIds] = createSignal<ReadonlySet<string>>(EMPTY)

  createEffect(() => {
    const current = ids()
    // Skip until the first read landed. Before an account is set, a write
    // throws (an account-scoped key has no namespace to resolve under), and
    // writing the empty placeholder would erase the stored set anyway.
    if (!seeded)
      return
    sessionStorageSet(KEY_EXPANDED_WORKSPACES, [...current])
  })

  return { expandedIds: ids, setExpandedIds: setIds }
})

function ensureSeeded(): void {
  if (seeded || !hasStorageAccount())
    return
  seeded = true
  setExpandedIds(readStoredIds())
}

// The namespace moved, so the mirror belongs to the account that left. Drop it
// and let the next access re-read under the new one. `setStorageAccount` calls
// this synchronously, before the identity signal notifies, so no render can
// observe the old set under the new account.
onStorageAccountChange(() => {
  seeded = false
  setExpandedIds(EMPTY)
})

/** Whether `workspaceId`'s tab tree is expanded. Reactive. */
export function isWorkspaceExpanded(workspaceId: string): boolean {
  ensureSeeded()
  return expandedIds().has(workspaceId)
}

/** Flip `workspaceId` between expanded and collapsed. */
export function toggleWorkspaceExpanded(workspaceId: string): void {
  ensureSeeded()
  setExpandedIds((prev) => {
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
export function setWorkspacesExpanded(workspaceIds: readonly string[], expanded: boolean): void {
  ensureSeeded()
  setExpandedIds((prev) => {
    const next = new Set(prev)
    let changed = false
    for (const id of workspaceIds) {
      if (expanded === next.has(id))
        continue
      changed = true
      if (expanded)
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
  seeded = false
  setExpandedIds(EMPTY)
}
