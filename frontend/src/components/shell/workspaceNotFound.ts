import type { createWorkspaceStore } from '~/stores/workspace.store'

interface WorkspaceNotFoundArgs {
  /** Workspace id from the URL. Empty/undefined on the home route. */
  workspaceId: string | undefined
  /** Authenticated user id. Empty until the session restore finishes. */
  userId: string
  workspaceState: ReturnType<typeof createWorkspaceStore>['state']
}

/**
 * True when the URL names a workspace that is genuinely absent -- it does not
 * exist, or it is not this user's.
 *
 * Every "we don't know yet" state answers false, because the caller renders a
 * dead-end 404 ("doesn't exist or you don't have access") off this: no load has
 * completed yet, the list still loading, the user not yet restored, and --
 * crucially -- a load that FAILED. A hub blip leaves `workspaces` empty, which is indistinguishable
 * from "you own nothing" by shape alone; telling the owner of a perfectly good
 * workspace that it doesn't exist is both wrong and unrecoverable without a
 * reload. `useWorkspaceLoader` records that failure on the store (and toasts
 * it), so the shell keeps rendering the workspace until a load succeeds.
 */
export function isWorkspaceNotFound(args: WorkspaceNotFoundArgs): boolean {
  if (!args.workspaceId)
    return false
  // Never-loaded is NOT empty. The store starts { loading: false,
  // workspaces: [] }, and useWorkspaceLoader only sets loading inside onMount,
  // which Solid defers past the first render -- so without this the very first
  // evaluation on a /workspace/:id load scores a genuine 404 for a workspace
  // the user owns. It is currently invisible because nothing paints in that
  // frame, which makes it a latent trap: a Suspense boundary, a reordering of
  // the loader, or a second reader of this predicate turns it into the rendered
  // page.
  if (!args.workspaceState.loaded)
    return false
  if (args.workspaceState.loading)
    return false
  if (!args.userId)
    return false
  if (args.workspaceState.error)
    return false
  return !args.workspaceState.workspaces.some(w => w.id === args.workspaceId)
}
