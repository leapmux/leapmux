import type { createAgentStore } from '~/stores/agent.store'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import { createEffect, untrack } from 'solid-js'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { detectFlavor, relativeUnder } from '~/lib/paths'
import { tabKey } from '~/stores/tab.store'

export interface SyncGitStatusToTabsOpts {
  gitFileStatusStore: ReturnType<typeof createGitFileStatusStore>
  tabStore: ReturnType<typeof createTabStore>
  agentStore: ReturnType<typeof createAgentStore>
}

/**
 * Sync `gitFileStatusStore` into matching tabs' git fields so the workspace
 * tab tree stays consistent with the directory tree after refreshes. Tabs
 * keep their last-known git fields across repo switches because the git
 * store only ever reflects ONE focused repo's state — without the stamp,
 * tabs from previously-focused repos would silently lose their diff stats
 * on workspace switch. Consumers (`WorkspaceTabTree`, `AppShellDialogs`)
 * therefore read the diff stats off `Tab` directly via
 * `diffStatsFromTabFields`, which is why this is a write-back effect
 * rather than a derived selector.
 *
 * Reads from `tabStore` and `agentStore` are wrapped in `untrack` so the
 * effect only re-runs when the git store changes — re-running on workspace
 * switch would apply stale git data from the previous workspace.
 *
 * Must be called inside a SolidJS reactive root (component body or
 * `createRoot`).
 */
export function syncGitStatusToTabs(opts: SyncGitStatusToTabsOpts): void {
  const { gitFileStatusStore, tabStore, agentStore } = opts
  createEffect(() => {
    const files = gitFileStatusStore.state.files
    const repoRoot = gitFileStatusStore.state.repoRoot
    const originUrl = gitFileStatusStore.state.originUrl
    const currentBranch = gitFileStatusStore.state.currentBranch
    if (!repoRoot)
      return
    let added = 0
    let deleted = 0
    let untracked = 0
    for (const f of files) {
      if (f.unstagedStatus === GitFileStatusCode.UNTRACKED) {
        untracked++
      }
      else {
        added += f.linesAdded + f.stagedLinesAdded
        deleted += f.linesDeleted + f.stagedLinesDeleted
      }
    }
    const gitFields = {
      gitDiffAdded: added,
      gitDiffDeleted: deleted,
      gitDiffUntracked: untracked,
      gitOriginUrl: originUrl || undefined,
      gitBranch: currentBranch || undefined,
    }
    const tabAlreadyMatches = (tab: Tab): boolean =>
      tab.gitDiffAdded === gitFields.gitDiffAdded
      && tab.gitDiffDeleted === gitFields.gitDiffDeleted
      && tab.gitDiffUntracked === gitFields.gitDiffUntracked
      && tab.gitOriginUrl === gitFields.gitOriginUrl
      && tab.gitBranch === gitFields.gitBranch
    const rootFlavor = detectFlavor(repoRoot)
    // Resolve the set of tabs that need new git fields, then apply via one
    // batched store mutation. Per-tab updateTab() calls walk the array each
    // time; with many tabs and many matches that becomes O(N·K).
    const targetKeys = new Set<string>()
    for (const tab of untrack(() => tabStore.state.tabs)) {
      let workingDir: string | undefined
      if (tab.type === TabType.AGENT)
        workingDir = untrack(() => agentStore.getById(tab.id))?.workingDir
      else if (tab.type === TabType.TERMINAL)
        workingDir = tab.workingDir
      else
        continue
      if (!workingDir)
        continue
      if (relativeUnder(workingDir, repoRoot, rootFlavor) === null)
        continue
      if (tabAlreadyMatches(tab))
        continue
      targetKeys.add(tabKey(tab))
    }
    if (targetKeys.size > 0)
      tabStore.updateMatchingTabs(t => targetKeys.has(tabKey(t)), gitFields)
  })
}
