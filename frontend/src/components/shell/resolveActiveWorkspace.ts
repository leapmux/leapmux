import type { createWorkspaceStore } from '~/stores/workspace.store'

export interface ResolveActiveWorkspaceArgs {
  /** Currently active workspace, or null when nothing is selected yet. */
  activeWorkspaceId: string | null
  /** Authenticated user id. Empty until the session restore finishes. */
  userId: string
  workspaceState: ReturnType<typeof createWorkspaceStore>['state']
  /**
   * The workspace this user was last on, read from localStorage. Undefined
   * on a first visit, or after the key expired.
   */
  savedWorkspaceId: string | undefined
}

/**
 * What the shell should do about its active-workspace selection.
 *
 * `keep` covers two different situations on purpose -- "the current selection
 * is fine" and "we don't know enough to say" -- because the action is the same
 * in both: leave the user where they are. Separating them would invite a caller
 * to treat the second as a decision.
 */
export type ActiveWorkspaceDecision
  = | { kind: 'keep' }
    | { kind: 'adopt', workspaceId: string }
    | { kind: 'clear' }

/**
 * Decides which workspace the shell should be showing.
 *
 * There is no workspace id in the URL, so this is the whole of that policy:
 * which one to open on load, which one to fall back to when the active one
 * disappears, and when to show the "no workspace" empty state.
 *
 * Every "we don't know yet" state answers `keep` -- no load has completed, one
 * is in flight, the user is not restored, or the last load FAILED. That last
 * one matters most: a hub blip leaves `workspaces` empty, which is
 * indistinguishable by shape from "you own nothing". Acting on it would yank
 * the user off a workspace they are actively using and, if they only own the
 * one, drop them on the create-a-workspace empty state until the next
 * successful load. `useWorkspaceLoader` records that failure on the store (and
 * toasts it), so the shell keeps rendering the current workspace until a load
 * succeeds.
 */
export function resolveActiveWorkspace(args: ResolveActiveWorkspaceArgs): ActiveWorkspaceDecision {
  // The store starts { loading: false, workspaces: [] }, and useWorkspaceLoader
  // only sets loading inside onMount, which Solid defers past the first render.
  // Without the `loaded` bit the very first evaluation reads as "loaded, and you
  // own nothing" and clears a perfectly good selection.
  if (!args.workspaceState.loaded)
    return { kind: 'keep' }
  if (args.workspaceState.loading)
    return { kind: 'keep' }
  if (!args.userId)
    return { kind: 'keep' }
  if (args.workspaceState.error)
    return { kind: 'keep' }

  const { workspaces } = args.workspaceState
  const active = args.activeWorkspaceId
  const has = (id: string) => workspaces.some(w => w.id === id)

  if (active && has(active))
    return { kind: 'keep' }

  // Past this point the selection is either absent or names a workspace this
  // user demonstrably does not have -- deleted from another device, archived
  // away, or a saved id that outlived its workspace.
  if (workspaces.length === 0)
    return active ? { kind: 'clear' } : { kind: 'keep' }

  const saved = args.savedWorkspaceId
  if (saved && has(saved))
    return { kind: 'adopt', workspaceId: saved }
  return { kind: 'adopt', workspaceId: workspaces[0].id }
}
