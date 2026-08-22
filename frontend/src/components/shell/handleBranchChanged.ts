import type { TabContext } from './tabContext'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { RepoRef } from '~/stores/tab.helpers'
import * as workerRpc from '~/api/workerRpc'
import { createLogger } from '~/lib/logger'
import { patchFromGetGitFileStatus } from '~/stores/repoGit'
import { isSameRepo } from '~/stores/tab.helpers'
import { stampBranchOnRepo } from './stampBranchOnTabs'

const log = createLogger('handleBranchChanged')

/**
 * What has to happen after a Change branch / Delete branch succeeds.
 *
 *   1. Stamp the new branch label on the repo-keyed store.
 *   2. Refresh diff stats for the affected repo (focused refresh when active).
 */
export interface BranchChangedDeps {
  repoGitStore: ReturnType<typeof createRepoGitStore>
  getCurrentTabContext: () => TabContext
}

export function handleBranchChanged(
  deps: BranchChangedDeps,
  repo: RepoRef,
  newBranch: string,
): void {
  if (!repo.workerId || !repo.gitToplevel)
    return

  stampBranchOnRepo(deps.repoGitStore, repo, newBranch)

  const active = isSameRepo(deps.getCurrentTabContext(), repo)
  if (active) {
    void deps.repoGitStore.refresh(repo.workerId, repo.gitToplevel)
      .catch((err) => {
        log.warn('failed to refresh git status after branch change', err)
      })
    return
  }

  void (async () => {
    try {
      const resp = await workerRpc.getGitFileStatus(repo.workerId, {
        workerId: repo.workerId,
        path: repo.gitToplevel,
      })
      const mapped = patchFromGetGitFileStatus(repo.workerId, resp)
      if (mapped)
        deps.repoGitStore.upsert(mapped.key, mapped.patch)
    }
    catch (err) {
      log.warn('failed to refresh git status after branch change', err)
    }
  })()
}
