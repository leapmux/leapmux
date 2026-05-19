import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
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
 * The subset of a {@link GetGitFileStatusResponse} required to stamp tab
 * fields. Lives here as its own shape so the imperative cross-repo
 * refresh path (`refreshGitStatusForTabs` in AppShell) can stamp tabs
 * without first writing into the gitFileStatusStore singleton — that
 * singleton describes a single focused repo, so calling its `refresh()`
 * for a non-active repo would clobber the file tree the user is looking
 * at while their branch-change action affects a different repo.
 *
 * `toplevel` is the worktree-aware working-tree root (worktree dir for
 * an in-worktree query, repo root otherwise). It's the identity used
 * for tab matching: a worktree's tabs carry `gitToplevel == toplevel`
 * while main-tree tabs carry `gitToplevel == repo_root`. Containment
 * MUST use this field, not the canonical repo root — otherwise a
 * focused worktree's branch gets stamped onto every main-tree tab.
 *
 * `repoRoot` is kept separately because `files` are reported relative
 * to it (the worker's git probes resolve `-C <dir>` internally to the
 * main repo), and the file-tree consumer reads `repoRoot` for path
 * resolution.
 */
export interface GitStatusForTabStamping {
  repoRoot: string
  toplevel: string
  originUrl: string
  currentBranch: string
  files: readonly GitFileStatusEntry[]
}

/**
 * Generic mutation surface a tab list exposes. The active workspace's
 * tabStore satisfies it directly; inactive workspaces (registry
 * snapshots) get a shim that rewrites the snapshot via
 * `WorkspaceStoreRegistry.update`. Lets `applyGitStatusToTabs` walk
 * either source with the same containment + diff-aggregation logic so
 * the active and inactive paths never drift.
 */
export interface TabStampTarget {
  tabs: readonly Tab[]
  update: (predicate: (t: Tab) => boolean, fields: Partial<Tab>) => void
}

/**
 * Stamp matching tabs with the diff/branch/origin fields from a
 * GetGitFileStatus response, without touching the gitFileStatusStore
 * singleton. The reactive {@link syncGitStatusToTabs} effect routes the
 * focused repo through this same helper; cross-repo callers (a Change /
 * Delete branch dialog opened against a non-active workspace's row)
 * call it directly to refresh diff badges on the affected repo's tabs.
 *
 * Accepts a {@link TabStampTarget} so the active tabStore AND inactive
 * registry snapshots can be stamped uniformly. AppShell stamps every
 * registry snapshot on a branch change so an inactive workspace's
 * sidebar tree picks up the new branch / diff stats without waiting
 * for a switch-in refresh.
 *
 * Containment / per-tab guard logic mirrors the reactive effect — see
 * that comment for why containment is path-based.
 */
export function applyGitStatusToTabs(
  target: TabStampTarget,
  status: GitStatusForTabStamping,
): void {
  const { repoRoot, toplevel, originUrl, currentBranch, files } = status
  // toplevel is the stamping identity. Empty toplevel = nothing to
  // anchor to (the worker didn't resolve a working tree); skip.
  if (!toplevel)
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
    gitToplevel: toplevel,
  }
  const tabAlreadyMatches = (tab: Tab): boolean =>
    tab.gitDiffAdded === gitFields.gitDiffAdded
    && tab.gitDiffDeleted === gitFields.gitDiffDeleted
    && tab.gitDiffUntracked === gitFields.gitDiffUntracked
    && tab.gitOriginUrl === gitFields.gitOriginUrl
    && tab.gitBranch === gitFields.gitBranch
    && tab.gitToplevel === gitFields.gitToplevel
  const rootFlavor = detectFlavor(toplevel)
  const targetKeys = new Set<string>()
  for (const tab of target.tabs) {
    const containmentPath = tab.workingDir
      ?? (tab.type === TabType.FILE ? tab.filePath : undefined)
    if (!containmentPath)
      continue
    if (tab.gitToplevel) {
      // Authoritative path: a tab whose toplevel is known sits in
      // exactly one working tree — the one whose root equals
      // gitToplevel. Worktrees and the main tree report the same
      // repo_root but DIFFERENT toplevels; matching on toplevel keeps
      // their branch/diff state independent. Without this, a focused
      // worktree's branch was being smeared across every main-tree
      // tab in the same repo (CHANGE/Create Worktree → switch focus
      // to the new worktree's agent → main-repo branch row's label
      // flipped to the worktree's branch).
      if (relativeUnder(tab.gitToplevel, toplevel, rootFlavor) === '') {
        // exact toplevel match: proceed to stamping
      }
      else if (
        // Pre-PR migration window: the old worker stamped
        // gitToplevel = repoRoot for both main-tree AND worktree tabs
        // (the toplevel field didn't exist yet). After upgrade, a
        // worktree refresh now reports toplevel = worktreeDir but
        // persisted tabs still hold gitToplevel = mainRepoRoot, so
        // the exact-match check above permanently skips them. Detect
        // this narrow case by: (a) the tab's stale stamp equals the
        // current status.repoRoot (signature only pre-PR code could
        // have produced — post-PR code stamps toplevel, and a tab
        // belonging to a nested inner repo would carry the inner's
        // own toplevel, not the parent's repoRoot), AND (b) the
        // tab's containment path actually sits under the current
        // toplevel. Both being true means this tab really belongs to
        // the currently-focused working tree; re-stamp.
        repoRoot
        && tab.gitToplevel === repoRoot
        && relativeUnder(containmentPath, toplevel, rootFlavor) !== null
      ) {
        // pre-PR migration: fall through and re-stamp with the
        // worktree-aware gitToplevel
      }
      else {
        continue
      }
    }
    // First-sync fallback for tabs that haven't learned their toplevel
    // yet — the path-under-toplevel check is the best we can do until
    // the next refresh stamps gitToplevel from above.
    else if (relativeUnder(containmentPath, toplevel, rootFlavor) === null) {
      continue
    }
    if (tabAlreadyMatches(tab))
      continue
    targetKeys.add(tabKey(tab))
  }
  if (targetKeys.size > 0)
    target.update(t => targetKeys.has(tabKey(t)), gitFields)
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

  // Set-of-entries that changes when a tab whose git stamp may need
  // (re)computing is added/removed/identity-shifted. Includes the
  // fields the effect actually reads for the containment + already-
  // stamped checks, so a drag/rename/status update doesn't churn it.
  //
  // `gitToplevel` is here for a reason that isn't the obvious "this
  // effect writes it": external broadcasts (TerminalStatusChange /
  // AgentStatusChange in `useWorkspaceConnection`, and the periodic
  // re-hydration in `useTabHydrators`) also write `gitToplevel`. A tab
  // that was created in a non-git dir and later `cd`-d into the focused
  // repo can have its `workingDir` stay the same while a worker re-probe
  // flips `gitToplevel` to repoRoot — without it in the signature, the
  // effect would not re-evaluate that tab. The self-trigger this causes
  // after the effect's own write is bounded (one O(N) walk, all skipped
  // by `tabAlreadyMatches`), and the broadcast-correctness case wins.
  //
  // Stored as a `Set<string>` so set-equality (size + membership) decides
  // whether to notify downstream — order-independent without paying for a
  // sort on every reactive tick (drags reorder `tabStore.state.tabs` via
  // `setTabsFromCrdt` without changing the underlying set).
  const unstampedTabsSignature = createMemo<Set<string>>(() => {
    const parts = new Set<string>()
    for (const tab of tabStore.state.tabs) {
      const containmentPath = tab.workingDir
        ?? (tab.type === TabType.FILE ? tab.filePath : undefined)
      if (!containmentPath)
        continue
      parts.add(`${tab.type}\0${tab.id}\0${containmentPath}\0${tab.gitToplevel ?? ''}`)
    }
    return parts
  }, new Set<string>(), {
    equals: (a, b) => {
      if (a === b)
        return true
      if (a.size !== b.size)
        return false
      for (const item of a) {
        if (!b.has(item))
          return false
      }
      return true
    },
  })

  createEffect(() => {
    // Tracked reads: re-run when any of these flip.
    const status: GitStatusForTabStamping = {
      repoRoot: gitFileStatusStore.state.repoRoot,
      toplevel: gitFileStatusStore.state.toplevel,
      originUrl: gitFileStatusStore.state.originUrl,
      currentBranch: gitFileStatusStore.state.currentBranch,
      files: gitFileStatusStore.state.files,
    }
    // Track the unstamped-tabs signature so the effect fires when a new
    // tab appears even if the git store state hasn't changed.
    void unstampedTabsSignature()
    // Untrack the inner tab walk: applyGitStatusToTabs reads tabStore.state
    // and writes back to it, which would otherwise self-trigger this effect
    // on every refresh. tabAlreadyMatches and a target-key set keep the
    // write quiet for no-op rows, so the self-trigger is bounded — but
    // explicit untrack documents the boundary.
    //
    // Note on `gitIsWorktree`: the store carries `isWorktree` for the
    // focused dir, but the value describes the QUERIED path, NOT
    // `repoRoot`. A worktree query and a main-tree query both return
    // the same `repoRoot` (the main repo root) while only the worktree
    // query reports `isWorktree=true`. Mass-stamping it onto every tab
    // whose gitToplevel matches repoRoot would mislabel sibling tabs
    // in the main repo as worktree tabs as soon as the worktree was
    // focused last. Per-tab worktree disposition must come from a
    // per-tab probe (inspect RPCs); applyGitStatusToTabs intentionally
    // omits it.
    untrack(() => applyGitStatusToTabs(tabStoreTarget(tabStore), status))
  })
}

/**
 * {@link TabStampTarget} adapter for the active workspace's tabStore.
 * Exposed so AppShell can pass the same instance to both the reactive
 * focused-repo sync and any imperative cross-repo refresh.
 */
export function tabStoreTarget(tabStore: ReturnType<typeof createTabStore>): TabStampTarget {
  return {
    get tabs() {
      return tabStore.state.tabs
    },
    update: (predicate, fields) => tabStore.updateMatchingTabs(predicate, fields),
  }
}
