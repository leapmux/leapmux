import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import type { Tab } from '~/stores/tab.types'
import type { TabMetadata, TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, createMemo, untrack } from 'solid-js'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { detectFlavor, relativeUnder } from '~/lib/paths'
import { sameKeys } from '~/lib/sameKeys'

export interface SyncGitStatusToTabsOpts {
  gitFileStatusStore: ReturnType<typeof createGitFileStatusStore>
  view: TabView
  metadata: TabMetadataStore
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
 * The canonical `repo_root` is deliberately NOT carried here. Stamping
 * only ever asks "which working tree is this tab in", which `toplevel`
 * answers exactly; a worktree query and a main-tree query return the
 * SAME `repo_root`, so admitting it invites matching on it by mistake.
 */
export interface GitStatusForTabStamping {
  /**
   * Worker this status came from. Repo identity is `(workerId, toplevel)` —
   * the same absolute path on two workers is the normal case, not a
   * pathological one, and this stamp reaches every workspace in the account.
   * `isSameRepo` already compares the pair; matching on path alone here would
   * smear one worker's branch and diff badges across the other's tabs.
   */
  workerId: string
  toplevel: string
  originUrl: string
  currentBranch: string
  files: readonly GitFileStatusEntry[]
}

/**
 * Project the git-status singleton onto the stamping shape.
 *
 * Two callers need exactly these five fields off `gitFileStatusStore.state` —
 * the reactive effect below and `handleBranchChanged`'s same-repo branch — and
 * each used to spell the mapping out by hand. The shape's own doc explains that
 * it deliberately omits `repoRoot` because admitting it "invites matching on it
 * by mistake"; a hand-copied projection re-opens exactly that the moment a
 * sixth field lands on the store.
 *
 * A free adapter rather than a store method on purpose: `gitFileStatusStore` is
 * a generic file-status store and should not learn the vocabulary of the tab
 * stamping layer.
 *
 * Reads are TRACKED — callers inside a `createEffect` depend on these five
 * fields, which is what makes the effect re-run when the focused repo changes.
 */
export function gitStatusFromStore(
  state: {
    workerId: string
    toplevel: string
    originUrl: string
    currentBranch: string
    files: readonly GitFileStatusEntry[]
  },
): GitStatusForTabStamping {
  return {
    workerId: state.workerId,
    toplevel: state.toplevel,
    originUrl: state.originUrl,
    currentBranch: state.currentBranch,
    files: state.files,
  }
}

/**
 * Generic "walk these tabs, write these fields" surface.
 *
 * It once had two implementations — the active workspace's tab store, and a
 * shim that rewrote each inactive workspace's registry snapshot — and existed
 * so the two could not drift. With every workspace's tabs joined from one
 * projection there is a single implementation ({@link tabStampTarget}), and the
 * interface survives as the seam that lets `applyGitStatusToTabs` be tested as
 * a pure function, with no CRDT bridge and no reactive root.
 */
export interface TabStampTarget {
  tabs: readonly Tab[]
  /**
   * `tabIds`, not a predicate. Both callers already walk `tabs` to decide what
   * matches, so handing back a predicate made the sole target re-materialize
   * the same set with a SECOND full walk of the account's tab list -- and
   * `patchMatching` then walked the metadata map a third time. Tab ids are
   * globally unique (`tabMetadata` is keyed by them), so the id set is the
   * natural currency here and `tabKey` was a pure detour.
   *
   * `TabMetadata`, not `Partial<Tab>`: the write lands in `tabMetadata`, whose
   * vocabulary differs from the assembled `Tab` — it has no `id`/`type`/
   * `tileId`/`position`/`workerId` (those come from the projection) and names
   * the terminal field `terminalStatus`, not `status`. Typed as `Partial<Tab>`
   * a caller could write `{ status }` or `{ tileId }`, typecheck, be written by
   * `patch`, and never be read back by `tabView.assemble`.
   */
  update: (tabIds: ReadonlySet<string>, fields: TabMetadata) => void
}

/**
 * Stamp matching tabs with the diff/branch/origin fields from a
 * GetGitFileStatus response, without touching the gitFileStatusStore
 * singleton. The reactive {@link syncGitStatusToTabs} effect routes the
 * focused repo through this same helper; cross-repo callers (a Change /
 * Delete branch dialog opened against a non-active workspace's row)
 * call it directly to refresh diff badges on the affected repo's tabs.
 *
 * Accepts a {@link TabStampTarget} rather than the stores directly, so the
 * helper stays a pure "match these tabs, write these fields" step that tests
 * can drive without a CRDT bridge. The one production target spans every
 * workspace, so a branch change refreshes a non-visible workspace's sidebar
 * branch label and diff badges without waiting for a switch-in refresh.
 *
 * Containment / per-tab guard logic mirrors the reactive effect — see
 * that comment for why containment is path-based.
 */
export function applyGitStatusToTabs(
  target: TabStampTarget,
  status: GitStatusForTabStamping,
): void {
  const { workerId, toplevel, originUrl, currentBranch, files } = status
  // (workerId, toplevel) is the stamping identity. Either one empty means
  // nothing to anchor to — the worker didn't resolve a working tree, or we
  // don't know which worker answered — and an unanchored stamp would match
  // across workers.
  if (!toplevel || !workerId)
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
  // `''`, not `undefined`, for the two clearable fields: the write goes through
  // `metadata.patch`, which SKIPS undefined so a partial row can't blank fields
  // another source owns. Sending undefined here means a repo that loses its
  // branch (detached HEAD, branch deleted) or its remote keeps the stale label
  // forever -- and, because `tabAlreadyMatches` then never holds, re-patches
  // every tab on every refresh without ever converging.
  const gitFields = {
    gitDiffAdded: added,
    gitDiffDeleted: deleted,
    gitDiffUntracked: untracked,
    gitOriginUrl: originUrl,
    gitBranch: currentBranch,
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
  const targetIds = new Set<string>()
  for (const tab of target.tabs) {
    // Same repo path on a different worker is a different repo.
    if ((tab.workerId ?? '') !== workerId)
      continue
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
      if (relativeUnder(tab.gitToplevel, toplevel, rootFlavor) !== '')
        continue
    }
    // First-sync fallback for tabs that haven't learned their toplevel
    // yet — the path-under-toplevel check is the best we can do until
    // the next refresh stamps gitToplevel from above.
    else if (relativeUnder(containmentPath, toplevel, rootFlavor) === null) {
      continue
    }
    if (tabAlreadyMatches(tab))
      continue
    targetIds.add(tab.id)
  }
  if (targetIds.size > 0)
    target.update(targetIds, gitFields)
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
 * containment check (`relativeUnder(containmentPath, toplevel)`)
 * filters out tabs whose paths don't sit under the old toplevel, which
 * covers the common case. A pathological setup where two workspaces
 * share overlapping file paths could still see a brief mis-stamp before
 * the refresh resolves and re-runs the effect with correct data.
 *
 * Must be called inside a SolidJS reactive root (component body or
 * `createRoot`).
 */
export function syncGitStatusToTabs(opts: SyncGitStatusToTabsOpts): void {
  const { gitFileStatusStore, view, metadata } = opts

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
  // flips `gitToplevel` to the repo's root — without it in the signature, the
  // effect would not re-evaluate that tab. The self-trigger this causes
  // after the effect's own write is bounded (one O(N) walk, all skipped
  // by `tabAlreadyMatches`), and the broadcast-correctness case wins.
  //
  // Stored as a `Set<string>` so set-equality (size + membership) decides
  // whether to notify downstream — order-independent without paying for a
  // sort on every reactive tick (drags reorder the projected tab list on
  // a projection tick without changing the underlying set).
  const unstampedTabsSignature = createMemo<Set<string>>(() => {
    const parts = new Set<string>()
    for (const tab of view.all()) {
      const containmentPath = tab.workingDir
        ?? (tab.type === TabType.FILE ? tab.filePath : undefined)
      if (!containmentPath)
        continue
      parts.add(`${tab.type}\0${tab.id}\0${containmentPath}\0${tab.gitToplevel ?? ''}`)
    }
    return parts
  }, new Set<string>(), {
    equals: sameKeys,
  })

  createEffect(() => {
    // Tracked reads: re-run when any of these flip.
    const status = gitStatusFromStore(gitFileStatusStore.state)
    // Track the unstamped-tabs signature so the effect fires when a new
    // tab appears even if the git store state hasn't changed.
    void unstampedTabsSignature()
    // Untrack the inner tab walk: applyGitStatusToTabs reads the tab list
    // and writes metadata back, which would otherwise self-trigger this effect
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
    untrack(() => applyGitStatusToTabs(tabStampTarget(view, metadata), status))
  })
}

/**
 * {@link TabStampTarget} over every workspace at once.
 *
 * There used to be two of these — one for the active `tabStore`, one that
 * patched registry snapshots for every other workspace — and `TabStampTarget`
 * existed so the two could not drift. With tabs joined from one projection
 * there is a single target and the abstraction has one implementation; it is
 * kept only as the test seam `applyGitStatusToTabs` is exercised through.
 *
 * The reach is wider than before by design: a branch rename now stamps tabs in
 * EVERY workspace in one call, instead of the active store plus a hand-rolled
 * fan-out across snapshots.
 */
export function tabStampTarget(view: TabView, metadata: TabMetadataStore): TabStampTarget {
  return {
    get tabs() {
      return view.all()
    },
    update: (tabIds, fields) => {
      // One `metadata.patch` per tab is one `setState` per tab, and the
      // branch-change paths in `AppShell` run from a promise continuation with
      // no ambient batch — so N matched tabs meant N full reactive flushes,
      // each re-running the `byWorkspace` join over every tab in the account.
      // One `patchMatching` is one flush.
      //
      // The caller resolves the id set, which also keeps the join OUT of this
      // write: a predicate evaluated inside `patchMatching`'s `produce` would
      // read the join and force a recompute after every write the same
      // `produce` had already made.
      if (tabIds.size === 0)
        return
      metadata.patchMatching((_meta, tabId) => tabIds.has(tabId), fields)
    },
  }
}
