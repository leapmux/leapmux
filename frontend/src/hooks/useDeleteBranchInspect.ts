import type { Accessor } from 'solid-js'
import type { InspectBranchDeletionResponse } from '~/generated/leapmux/v1/git_pb'
import { createSignal, onMount } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { useOrg } from '~/context/OrgContext'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { createLogger } from '~/lib/logger'

const log = createLogger('useDeleteBranchInspect')

export interface UseDeleteBranchInspectArgs {
  workerId: string
  gitToplevel: string
  /**
   * Branch label the calling row already knows. Threaded to the worker
   * as `branchNameHint` so it can parallelize `queryGitPathInfo` and
   * `pushStatusForPath`. Empty string for the "(no branch)" sidebar row.
   */
  branchName: string | null
  /**
   * Error sink for the dialog's banner. The hook also logs the
   * underlying error before delegating.
   */
  onError: (err: unknown) => void
}

export interface DeleteBranchInspect {
  /** Latest InspectBranchDeletion response, or null until the first RPC lands. */
  info: Accessor<InspectBranchDeletionResponse | null>
  /** True while a fetch is in flight. */
  loading: Accessor<boolean>
  /** Refire the bundle RPC (used after a successful push refresh). */
  refresh: () => Promise<void>
}

/**
 * One-shot bundle probe for DeleteBranchDialog. Mirrors
 * {@link useChangeBranchInspect} for the deletion flow: issues a single
 * `InspectBranchDeletion` RPC on mount, owns the resulting signal +
 * loading state, and exposes `refresh()` so the dialog can re-probe
 * after a push lands.
 */
export function useDeleteBranchInspect(args: UseDeleteBranchInspectArgs): DeleteBranchInspect {
  const org = useOrg()
  const [info, setInfo] = createSignal<InspectBranchDeletionResponse | null>(null)

  const fetcher = createGuardedFetch<void, InspectBranchDeletionResponse>({
    fetch: (_args, signal) => workerRpc.inspectBranchDeletion(args.workerId, {
      orgId: org.orgId(),
      workerId: args.workerId,
      path: args.gitToplevel,
      branchNameHint: args.branchName ?? '',
    }, { signal }),
    applySuccess: resp => setInfo(resp),
    onError: (err) => {
      log.warn('inspectBranchDeletion failed', err)
      args.onError(err)
    },
  })

  onMount(() => {
    // Defense-in-depth: callers must resolve a real gitToplevel before
    // opening the dialog (WorkspaceTabTree hides the menu when the
    // branch row's gitToplevel is empty). If we reach here without one,
    // skip the RPC — the worker's SanitizePath would reject `path: ""`
    // anyway, and surfacing a clear error keeps the dialog from looking
    // like a permission failure on a real repo.
    if (!args.gitToplevel) {
      const err = new Error('Cannot inspect branch deletion: tab has no resolved repo path yet')
      log.warn('inspectBranchDeletion skipped', err)
      args.onError(err)
      return
    }
    void fetcher.run()
  })

  return {
    info,
    loading: fetcher.loading,
    refresh: () => fetcher.run(),
  }
}
