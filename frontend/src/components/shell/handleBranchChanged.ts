import type { TabContext } from './tabContext'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { RepoRef } from '~/stores/tab.helpers'
import { createLogger } from '~/lib/logger'
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
  stampBranchOnRepo(deps.repoGitStore, repo, newBranch)

  void deps.repoGitStore.refresh(repo.workerId, repo.gitToplevel)
    .catch((err) => {
      log.warn('failed to refresh git status after branch change', err)
    })
}
