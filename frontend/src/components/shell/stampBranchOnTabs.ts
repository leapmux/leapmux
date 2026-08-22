import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { RepoRef } from '~/stores/tab.helpers'
import { repoKey } from '~/stores/repoGit'

/**
 * Re-label one repo's branch in the keyed git store, immediately.
 *
 * A Change/Delete branch dialog knows the new branch the moment its RPC
 * returns. This writes it straight through instead of waiting for the next
 * git-status refresh.
 */
export function stampBranchOnRepo(
  repoGitStore: ReturnType<typeof createRepoGitStore>,
  repo: RepoRef,
  newBranch: string,
): boolean {
  if (!repo.workerId || !repo.gitToplevel)
    return false
  const key = repoKey(repo.workerId, repo.gitToplevel)
  const prev = repoGitStore.get(key)
  if (prev?.branch === newBranch)
    return false
  repoGitStore.upsert(key, { branch: newBranch })
  return true
}
