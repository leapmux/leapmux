import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { stripRemotePrefix } from '~/lib/validate'

/**
 * Returns the local-branch name a successful checkout left the working
 * directory on. Only strips the remote prefix when we have positive
 * evidence the picked entry is a remote-tracking ref — either the
 * inspect RPC labelled it `isRemote: true`, or the branches list is
 * unavailable AND no local-shaped match was seen (best-effort fallback
 * for the rare race where the picker fired before branches refreshed).
 *
 * Critically, when the branches list IS available but the picked entry
 * isn't in it (refresh dropped it, the entry was a transient state),
 * stay verbatim rather than fall through to stripping. A local branch
 * whose name happens to contain `/` (e.g. `feature/auth`) would
 * otherwise be silently stamped as `auth`, splitting the sidebar group
 * and producing labels that don't match the ref the worker actually
 * checked out. Both ChangeBranchDialog and DeleteBranchDialog route
 * their post-mutation stamp through this helper so the sidebar label
 * matches HEAD in either flow.
 */
export function resolveStampedBranch(target: string, branches: GitBranchEntry[] | null): string {
  if (branches) {
    const entry = branches.find(b => b.name === target)
    if (entry?.isRemote)
      return stripRemotePrefix(target)
    // entry is local OR missing — both cases keep `target` verbatim.
    // For the missing case this avoids the prior strip-on-fallback that
    // mis-stamped local branches containing `/` as just their suffix.
    return target
  }
  // No branches list available: strip optimistically. The picker UI
  // can't have produced a meaningful selection without a list anyway,
  // so this path is only hit for synthetic callers (tests, future
  // programmatic uses).
  return stripRemotePrefix(target)
}
