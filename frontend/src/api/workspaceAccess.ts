import { channelClient } from './clients'

/**
 * "Does the worker's channel know about this workspace yet?"
 *
 * A worker channel is seeded with the user's accessible workspaces ONCE, at
 * `OpenChannel` time (`accessibleWorkspaceIds` on `ChannelOpenRequest`). The set
 * only ever grows afterwards through `PrepareWorkspaceAccess`, which fans a
 * `ChannelAccessUpdate` out to every channel the user holds on that worker and
 * acks only after each worker-side `AddAccessibleWorkspaceID` has run. So a
 * workspace that comes into existence AFTER a channel opened -- created by
 * `leapmux remote workspace create`, by another browser session, by anything
 * that is not this page's new-workspace dialog -- is invisible to that channel
 * until somebody announces it, and every workspace-scoped RPC on it is refused.
 *
 * Announcing is therefore a repair action, not a poll: the thing being waited
 * on is a transition nothing else will perform. Callers that hit the refusal
 * (`TAB_HYDRATION_STATUS_NOT_ACCESSIBLE` on a hydration batch, `PermissionDenied`
 * on a workspace-scoped stream) call `ensure` and then re-issue their request.
 *
 * Announcements are remembered for the life of the page, which is exactly as
 * long as they are true:
 *
 *   - a channel that stays open keeps the id -- `AddAccessibleWorkspaceID` is
 *     add-only and `channelAuthorizer.AccessibleSet` reads it live; and
 *   - a channel that drops and reopens re-seeds from the hub's DB, which by
 *     then contains the workspace.
 *
 * Failures are NOT remembered, so a later caller retries rather than inheriting
 * a permanent "already tried" verdict from an unrelated transient error.
 */
export interface WorkspaceAccessAnnouncer {
  /**
   * Make `workspaceId` accessible on `workerId`'s channels, at most once per
   * pair per page.
   *
   * Resolves `true` when THIS call is the one that completed a fresh
   * announcement, and `false` when the pair had already been announced. Callers
   * use that to decide whether re-issuing the refused request can plausibly
   * succeed: after a fresh announcement it can, and after `false` it cannot --
   * the channel already knew, so the refusal has some other cause and an
   * immediate retry would only be refused again.
   *
   * Concurrent callers for the same pair share one in-flight RPC and all see
   * `true`: each of them was refused before the announcement landed, so each of
   * them has a reason to retry.
   *
   * Rejects if the announcement fails.
   */
  ensure: (workerId: string, workspaceId: string) => Promise<boolean>
}

/**
 * Build an announcer over an arbitrary `prepare` implementation. Exists so the
 * memoization can be tested without a transport, and so a test gets a clean
 * cache instead of whatever earlier cases left in the module-level one.
 */
export function createWorkspaceAccessAnnouncer(
  prepare: (workerId: string, workspaceId: string) => Promise<unknown>,
): WorkspaceAccessAnnouncer {
  // Nested maps keyed by worker, then workspace -- NOT a joined
  // `${workerId}:${workspaceId}` string. A separator that occurs inside either
  // id makes two different pairs collide, and a collision here grants or denies
  // access against the wrong workspace. Nesting removes the encoding question
  // instead of picking a separator and hoping.
  const announced = new Map<string, Set<string>>()
  const inflight = new Map<string, Map<string, Promise<boolean>>>()

  const remember = (workerId: string, workspaceId: string): void => {
    let ids = announced.get(workerId)
    if (!ids) {
      ids = new Set()
      announced.set(workerId, ids)
    }
    ids.add(workspaceId)
  }

  const forget = (workerId: string, workspaceId: string): void => {
    const pending = inflight.get(workerId)
    if (!pending)
      return
    pending.delete(workspaceId)
    if (pending.size === 0)
      inflight.delete(workerId)
  }

  const ensure = async (workerId: string, workspaceId: string): Promise<boolean> => {
    if (announced.get(workerId)?.has(workspaceId))
      return false

    const pending = inflight.get(workerId)?.get(workspaceId)
    if (pending)
      return pending

    // Register the in-flight entry before the first await so a second caller in
    // the same tick joins this attempt instead of starting its own.
    const attempt = prepare(workerId, workspaceId)
      .then(() => {
        remember(workerId, workspaceId)
        return true
      })
      .finally(() => forget(workerId, workspaceId))

    let byWorkspace = inflight.get(workerId)
    if (!byWorkspace) {
      byWorkspace = new Map()
      inflight.set(workerId, byWorkspace)
    }
    byWorkspace.set(workspaceId, attempt)
    return attempt
  }

  return { ensure }
}

/**
 * Announce a workspace on a worker's channels via the hub.
 *
 * The page-wide instance. Everything that needs a workspace to be visible on a
 * worker channel goes through this one, so the "already announced" answer is
 * shared rather than re-derived per call site.
 */
export const ensureWorkspaceAccess: WorkspaceAccessAnnouncer['ensure'] = createWorkspaceAccessAnnouncer(
  (workerId, workspaceId) => channelClient.prepareWorkspaceAccess({ workerId, workspaceId }),
).ensure
