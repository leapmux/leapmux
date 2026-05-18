import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import type { createTabStore } from '~/stores/tab.store'
import type { Tab } from '~/stores/tab.types'
import { createEffect, createMemo, untrack } from 'solid-js'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { detectFlavor, relativeUnder } from '~/lib/paths'
import { tabKey } from '~/stores/tab.helpers'

export interface SyncGitStatusToTabsOpts {
  gitFileStatusStore: ReturnType<typeof createGitFileStatusStore>
  tabStore: ReturnType<typeof createTabStore>
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
 * The effect re-runs on two distinct triggers:
 *   1. Git store updates (refresh completed, or repo state changed).
 *   2. A new tab appears that hasn't been stamped yet — covered by the
 *      `unstampedTabsSignature` memo below. Without this, opening a file
 *      in the already-focused repo leaves the new FILE tab ungrouped
 *      because the store state doesn't change (same files, same branch)
 *      so the store-driven trigger never fires.
 *
 * The signature memo only changes when the SET of tabs-needing-stamping
 * actually changes, so unrelated tab mutations (drag, rename, status
 * update) don't re-walk the tab list.
 *
 * Workspace-switch stale-data note: a workspace switch swaps the tab
 * list synchronously but `gitFileStatusStore.refresh()` is async, so
 * briefly the store still reflects the previous workspace. The
 * containment check (`relativeUnder(containmentPath, repoRoot)`)
 * filters out tabs whose paths don't sit under the old repoRoot, which
 * covers the common case. A pathological setup where two workspaces
 * share overlapping file paths could still see a brief mis-stamp before
 * the refresh resolves and re-runs the effect with correct data.
 *
 * Must be called inside a SolidJS reactive root (component body or
 * `createRoot`).
 */
export function syncGitStatusToTabs(opts: SyncGitStatusToTabsOpts): void {
  const { gitFileStatusStore, tabStore } = opts

  // Fingerprint that changes when a tab whose git stamp may need
  // (re)computing is added/removed/identity-shifted. Includes the
  // fields the effect actually reads for the containment + already-
  // stamped checks, so a drag/rename/status update doesn't churn it.
  const unstampedTabsSignature = createMemo(() => {
    const parts: string[] = []
    for (const tab of tabStore.state.tabs) {
      const containmentPath = tab.workingDir
        ?? (tab.type === TabType.FILE ? tab.filePath : undefined)
      if (!containmentPath)
        continue
      parts.push(`${tab.type}\0${tab.id}\0${containmentPath}\0${tab.gitToplevel ?? ''}`)
    }
    parts.sort()
    return parts.join('\n')
  })

  createEffect(() => {
    const files = gitFileStatusStore.state.files
    const repoRoot = gitFileStatusStore.state.repoRoot
    const originUrl = gitFileStatusStore.state.originUrl
    const currentBranch = gitFileStatusStore.state.currentBranch
    // Track the unstamped-tabs signature so the effect fires when a new
    // tab appears even if the git store state hasn't changed.
    void unstampedTabsSignature()
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
      gitToplevel: repoRoot,
    }
    const tabAlreadyMatches = (tab: Tab): boolean =>
      tab.gitDiffAdded === gitFields.gitDiffAdded
      && tab.gitDiffDeleted === gitFields.gitDiffDeleted
      && tab.gitDiffUntracked === gitFields.gitDiffUntracked
      && tab.gitOriginUrl === gitFields.gitOriginUrl
      && tab.gitBranch === gitFields.gitBranch
      && tab.gitToplevel === gitFields.gitToplevel
    const rootFlavor = detectFlavor(repoRoot)
    // Resolve the set of tabs that need new git fields, then apply via one
    // batched store mutation. Per-tab updateTab() calls walk the array each
    // time; with many tabs and many matches that becomes O(N·K).
    const targetKeys = new Set<string>()
    for (const tab of untrack(() => tabStore.state.tabs)) {
      // Path used for the containment-against-repoRoot check. AGENT
      // and TERMINAL tabs carry `workingDir`. FILE tabs created in the
      // live session also carry it (set by `useTabOperations.openFile`),
      // but tabs restored after a refresh come back with only
      // `filePath` filled in by the path hydrator — the CRDT projection
      // doesn't carry `workingDir` and the hydrator doesn't set it.
      // `filePath` works as a stand-in here because `relativeUnder`
      // just answers "is X under Y?" — a file under the repo root and
      // a directory under the repo root are equally valid signals that
      // the tab belongs to the repo.
      const containmentPath = tab.workingDir
        ?? (tab.type === TabType.FILE ? tab.filePath : undefined)
      if (!containmentPath)
        continue
      // Once a tab has its own gitToplevel, that's the authoritative repo
      // identity — only stamp when it exactly matches the focused repo.
      // Path containment is a leaky proxy: a nested git repo lives inside
      // its parent's tree but is a *different* repo, so the parent's
      // identity must not be applied to it. relativeUnder(x, x) === ''
      // gives us exact equality with the right path-flavor handling.
      if (tab.gitToplevel) {
        if (relativeUnder(tab.gitToplevel, repoRoot, rootFlavor) !== '')
          continue
      }
      // First-sync fallback for tabs that haven't learned their toplevel
      // yet — the path-under-repoRoot check is the best we can do until
      // the next refresh stamps gitToplevel from above.
      else if (relativeUnder(containmentPath, repoRoot, rootFlavor) === null) {
        continue
      }
      if (tabAlreadyMatches(tab))
        continue
      targetKeys.add(tabKey(tab))
    }
    if (targetKeys.size > 0)
      tabStore.updateMatchingTabs(t => targetKeys.has(tabKey(t)), gitFields)
  })
}
