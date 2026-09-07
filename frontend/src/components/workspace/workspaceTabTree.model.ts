/**
 * The pure model behind the sidebar's tab tree: how tabs group into repository
 * and branch rows, how a subagent nests under its parent, and the fingerprint
 * that decides when the tree rebuilds.
 *
 * Separate from `./WorkspaceTabTree.tsx` on purpose. Nothing here touches a
 * Solid component, so a test, a store, or another surface reads the tree's
 * shape without loading four row components -- which is also why `branchActions`
 * and `repoCheckouts` can take their types from a module they sit beside
 * instead of from the renderer above them.
 */
import type { PathFlavor } from '~/lib/paths'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { RepoGitStore } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { shallowEqualExcept } from '~/lib/shallowEqual'
import { tildifyForWorker, workerPathFlavor } from '~/lib/workerPaths'
import { repoGitView } from '~/stores/repoGit'
import { isSubagentTab, tabKey } from '~/stores/tab.helpers'
import { isAgentTab } from '~/stores/tab.types'
import {
  branchKey,
  branchNameSegment,
  isLocalRepoKey,
  repoKeyAndLabel,
  tabBranchKey,
  tabGitToplevelForKey,
} from './branchKeys'

/**
 * Display fallback for tabs whose git state has no branch name yet
 * (e.g. detached HEAD or a freshly-initialised repo). Rendered only at
 * the display layer — internally a missing branch is represented as
 * `null` so it can never collide with a real branch literally named
 * `(no branch)`.
 */
export const NO_BRANCH_LABEL = '(no branch)'

export function branchGroupKey(b: BranchGroup): string {
  return branchKey(b.branchName, b.workerId, b.gitToplevel)
}

// Compact per-tab fingerprint used by tabsProjection. Mirrors every field
// `buildTree` reads from a Tab, joined with `\0` so adjacent field
// boundaries are unambiguous: pathnames, branch names, ids, and origin
// URLs can all contain `|` but never a literal NUL byte, so two distinct
// (gitToplevel, gitOriginUrl) pairs can't share a fingerprint by sliding
// across the separator. The leading id keeps every fingerprint unique
// across tabs regardless of the other fields. Exported for unit tests to
// pin the field-coverage contract.
//
// STRUCTURE ONLY. The fields here are the ones that decide which group a tab
// lands in, what order it sits in, and what the branch/repo diff badges add up
// to. Everything a ROW renders -- title, agent provider, terminal status, PTY
// title, progress -- is deliberately absent, and must never be added: those
// change at PTY-read and status-push frequency, and rebuilding the whole tree
// on each one is precisely what this gate exists to prevent. The rows read
// those fields from the live lookup instead (see `TabLeafList`), so a field
// missing here is not a field that goes stale.
export function tabBuildKey(t: Tab, store: RepoGitStore): string {
  const git = repoGitView(t, store)
  return [
    t.id,
    // `type` because the ROW key is `${type}:${id}` (tabKey), not the id alone.
    // Leaving it out lets the cached key list hold a key the live lookup can
    // never resolve if a tab's type ever changes in place -- and now that a
    // subagent row renders INSIDE its parent's guard, an unresolvable parent
    // takes its whole subtree out of the sidebar with it, not just one row.
    t.type,
    // Structure: a subagent tab renders UNDER its parent, so the link decides
    // where the row sits. It is written once (undefined -> id, at hydration),
    // so including it costs one rebuild per subagent rather than churn.
    isAgentTab(t) ? t.parentAgentId ?? '' : '',
    t.workerId ?? '',
    git.branchLabel ?? '',
    // The same resolution the tree itself uses, so the fingerprint matches
    // buildTree / tabBranchKey -- including when the store says "not a git
    // repository" and the row's stale toplevel must not win. The view is
    // already resolved above; passing it in saves the second resolution per
    // key build.
    tabGitToplevelForKey(t, store, git),
    git.isWorktree ? '1' : '0',
    git.originUrl ?? '',
    git.diffStats.added,
    git.diffStats.deleted,
    git.diffStats.untracked,
    t.tileId ?? '',
    t.position ?? '',
  ].join('\0')
}

/** One worker id and the info the tree resolved for it. */
export interface WorkerProjectionEntry {
  id: string
  info: WorkerInfo | null
}

/**
 * Equality for `workersProjection`.
 *
 * It compares EVERY `WorkerInfo` field except `updatedAt`, rather than the few
 * that `buildTree` happens to read. A read-list is a second source of truth:
 * add a field to `buildTree`, forget the list, and the tree freezes at that
 * field's pre-RPC value exactly as `homeDir` did. Comparing every field can
 * only ask for MORE rebuilds than the tree needs, never fewer, so a field the
 * tree ignores costs one extra rebuild when it changes — never a stale row.
 *
 * `updatedAt` is the one exclusion. The store stamps a fresh one on every TTL
 * probe and keeps the previous record when nothing else moved, so tracking it
 * would rebuild the whole tree on a no-op refetch.
 *
 * Per-FIELD comparison, not `Object.is` on the record: `workerInfoFn` is an
 * arbitrary prop, and a caller that allocates a fresh record per call — which
 * every test in this file does — would make an identity-based memo never
 * settle.
 */
export function workerProjectionsEqual(
  a: readonly WorkerProjectionEntry[],
  b: readonly WorkerProjectionEntry[],
): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    if (a[i].id !== b[i].id)
      return false
    const x = a[i].info
    const y = b[i].info
    if (x === y)
      continue
    if (!x || !y)
      return false
    if (!shallowEqualExcept(x, y, ['updatedAt']))
      return false
  }
  return true
}

/**
 * Snapshot one branch row for the change and the delete dialog.
 *
 * `tabs` is re-resolved through the LIVE lookup rather than handed straight
 * from `b.tabs`: the branch group is a cached structure (see `tabBuildKey`), so
 * its own `Tab` objects can predate the last hydration or rename. Both dialogs
 * freeze what they get at open time -- `DeleteBranchDialog` counts tabs by type
 * and reads a `workingDir` off one of them -- so the snapshot they freeze had
 * better be the current one. `liveTabs` drops a key that no longer resolves,
 * which identifies a tab closed since the last rebuild; that is the point, not
 * a loss.
 */
export function buildBranchRef(workspaceId: string, b: BranchGroup, liveTabs: (tabs: readonly Tab[]) => Tab[]): BranchRef {
  return {
    workspaceId,
    workerId: b.workerId,
    gitToplevel: b.gitToplevel,
    isWorktree: b.isWorktree,
    branchName: b.branchName,
    tabs: liveTabs(b.tabs),
  }
}

// --- Subagent nesting ---

/** One row of the sidebar's tab tree, plus the subagent rows hanging under it. */
export interface TabNode {
  tab: Tab
  children: TabNode[]
}

function parentAgentIdOf(tab: Tab): string | undefined {
  // The VALUE, not the boolean: the nesting walk needs the id it points at.
  // `isSubagentTab` answers the same question one bit narrower, so ask it first
  // and keep the two spellings of "this tab has a parent" in agreement.
  return isSubagentTab(tab) && isAgentTab(tab) ? tab.parentAgentId : undefined
}

/**
 * Fold a flat, already-sorted tab list into the subagent tree the sidebar draws.
 *
 * A child agent tab (one with `parentAgentId`) hangs under its parent when that
 * parent is in the SAME list; otherwise it stays at the top level, because the
 * parent tab is closed or sits in a different branch group and there is no row
 * to hang it from. Nesting is per direct parent only -- a subagent whose own
 * subagent is open renders two levels deep, but a tab whose parent is absent is
 * NOT re-parented onto a surviving grandparent, which would claim a lineage the
 * user cannot see.
 *
 * Order within every level is the input order, so the caller's sort still
 * decides it. Pure: it reads only the fields above and allocates a fresh forest.
 */
export function nestSubagentTabs(tabs: readonly Tab[]): TabNode[] {
  // Keyed by the composite tabKey, not by a bare id. `tabKey` is namespaced by
  // TYPE precisely because an AGENT and a TERMINAL tab can share an id, and a
  // parent link only ever identifies an AGENT -- so a bare-id lookup let a non-agent
  // tab resolve to the agent's node, push it into the forest twice, and drop the
  // non-agent row entirely.
  const agentNodeKey = (id: string) => tabKey({ type: TabType.AGENT, id } as Tab)
  const nodeByKey = new Map<string, TabNode>()
  for (const tab of tabs) {
    if (isAgentTab(tab))
      nodeByKey.set(tabKey(tab), { tab, children: [] })
  }

  // True when walking up from `from` reaches `targetId`. Guards against a
  // parent cycle: the worker cannot produce one (parent_agent_id is a DAG
  // rooted at a main agent), but attaching both ends of a cycle would recurse
  // forever in the renderer, so a suspect link demotes the tab to a root
  // instead. The visited set also limits a chain that repeats for any reason.
  const reaches = (from: TabNode, targetId: string): boolean => {
    const seen = new Set<string>()
    let id = parentAgentIdOf(from.tab)
    while (id && !seen.has(id)) {
      if (id === targetId)
        return true
      seen.add(id)
      const next = nodeByKey.get(agentNodeKey(id))
      id = next ? parentAgentIdOf(next.tab) : undefined
    }
    return false
  }

  const roots: TabNode[] = []
  for (const tab of tabs) {
    const node = nodeByKey.get(tabKey(tab)) ?? { tab, children: [] }
    const parentId = parentAgentIdOf(tab)
    const parent = parentId ? nodeByKey.get(agentNodeKey(parentId)) : undefined
    if (parent && parent !== node && !reaches(parent, tab.id))
      parent.children.push(node)
    else
      roots.push(node)
  }
  return roots
}

/**
 * Identifies a branch row for both the Change Branch and Delete Branch
 * dialogs. The two dialogs read overlapping subsets — Change reads
 * `workspaceId` + branch identity; Delete reads branch identity + tab
 * snapshot — and ignore the rest. A merged ref keeps the call site
 * simple (one shape, populated once from the branch row).
 *
 * `branchName` is `null` when the row groups tabs that have no current
 * branch (the sidebar's "(no branch)" bucket).
 */
export interface BranchRef {
  workspaceId: string
  workerId: string
  gitToplevel: string
  /**
   * True iff `gitToplevel` resolves to a linked worktree. Threaded to
   * ChangeBranchDialog so it can seed `isWorktreeRoot`/`isRepoRoot`
   * correctly before the inspect RPC lands — without this a worktree-
   * opened dialog briefly paints a main-repo shape and downstream
   * GitOptions memos compute against the wrong fields.
   */
  isWorktree: boolean
  branchName: string | null
  tabs: Tab[]
}

// --- Grouping logic ---

export interface BranchGroup {
  /**
   * Real branch name, or `null` for tabs without a branch yet. The
   * display layer renders `null` as `NO_BRANCH_LABEL`.
   */
  branchName: string | null
  /**
   * Worker that owns the tabs in this group. Tabs in different workers
   * land in separate groups even when their gitOriginUrl matches and the
   * branch name is the same.
   */
  workerId: string
  /** Working-tree root of the tabs in this group (resolved per worker). */
  gitToplevel: string
  /**
   * True iff this group's gitToplevel resolves to a linked worktree.
   * Lifted from any tab in the group — all tabs in a `(workerId,
   * gitToplevel)` bucket share the same worker view of the same path,
   * so the disposition is uniform. ChangeBranchDialog reads this to
   * seed its path-info shape before the inspect RPC lands.
   */
  isWorktree: boolean
  /**
   * Branch label shown in the row. Equal to `branchName` when this is
   * the only group with that name within its repo; otherwise suffixed
   * with `(worker)`, `(~/path)`, or `(worker, ~/path)` depending on
   * which dimensions vary between the colliding groups.
   */
  displayLabel: string
  /**
   * The owning worker's home directory and path flavor, for the tilde
   * compression the row's tooltip applies to `gitToplevel`.
   *
   * Resolved in `buildTree` because only that function holds `workerInfoFn`;
   * the row itself has no route to a worker's home dir. They are the RAW
   * inputs, not a pre-shortened path: `WorkingTreeRows` owns the compression
   * rule for every surface, so the sidebar must not apply a second copy of it.
   * `homeDir` is empty and `flavor` is undefined until the worker's system
   * info arrives, which leaves the path absolute -- correct, and long.
   */
  homeDir: string
  flavor: PathFlavor | undefined
  /**
   * The worker's display name, but ONLY when this branch name appears on more
   * than one worker inside its repo. Empty otherwise.
   *
   * The same test the visible `displayLabel` suffix uses, so the row and its
   * tooltip agree on when the worker matters. Without it two workers holding
   * the same branch at the same path under each home directory produce two
   * rows whose tooltips are byte-identical, and Delete on one of them removes
   * the other machine's directory.
   */
  workerLabel: string
  tabs: Tab[]
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
}

export interface RepoGroup {
  repoKey: string
  repoLabel: string
  branches: BranchGroup[]
  /**
   * Per-row lookup map keyed by `branchKey(branchName, workerId, gitToplevel)`.
   * Built once during `buildTree` so each row's `<For>` body doesn't have to
   * rebuild its own Map on every reactive tick.
   */
  branchByKey: Map<string, BranchGroup>
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
}

export interface TabTree {
  groups: RepoGroup[]
  ungrouped: Tab[]
}

/**
 * Whether any of these tabs carries the unseen-activity marker.
 *
 * A COLLAPSED row shows one dot for everything folded under it. Collapsing
 * hides the leaf rows -- the wrapper only sets `visibility: hidden`, so they
 * stay in the DOM -- and the marker exists precisely for the workspace that
 * nobody looks at, so without the roll-up it is invisible in the case it is
 * for.
 *
 * Pass LIVE tabs, which `liveTabs` on the row-selection context resolves. A
 * group's own tab objects come from the cached tree, whose fingerprint
 * deliberately excludes `hasNotification` (see `tabBuildKey`), so a cached
 * object holds whatever the answer was at the last rebuild.
 */
export function anyTabHasNotification(tabs: readonly Tab[]): boolean {
  return tabs.some(t => t.hasNotification === true)
}

/**
 * Sum diff stats across the branch-groups a tab list would form, without
 * the full buildTree machinery. `buildTree` derives per-branch diff stats
 * by taking the first tab with non-zero stats in each `(branchName, workerId,
 * gitToplevel)` bucket (every tab in a bucket shares the same git state),
 * then sums those across branches. Callers that only need the workspace's
 * top-line diff badge can use this helper instead of allocating the full
 * group/branch structure.
 */
export function sumDiffStatsFromTabs(tabs: Tab[], store: RepoGitStore): { added: number, deleted: number, untracked: number } {
  const seen = new Set<string>()
  let added = 0
  let deleted = 0
  let untracked = 0
  for (const t of tabs) {
    const git = repoGitView(t, store)
    if (!git.originUrl && !git.toplevel && !t.gitToplevel)
      continue
    const { added: a, deleted: d, untracked: u } = git.diffStats
    if (a === 0 && d === 0 && u === 0)
      continue
    // The view resolved once above and threads in: the branch key used to
    // resolve it two more times per tab.
    const key = tabBranchKey(t, store, git)
    if (seen.has(key))
      continue
    seen.add(key)
    added += a
    deleted += d
    untracked += u
  }
  return { added, deleted, untracked }
}

export function buildTree(
  tabs: Tab[],
  store: RepoGitStore,
  tileOrder?: readonly string[],
  workerInfoFn?: (id: string) => WorkerInfo | null,
): TabTree {
  // Per-branch sort needs O(1) tile-index lookup; build the map once
  // here and reuse for every branch / the ungrouped bucket.
  const tileIndex = new Map<string, number>()
  if (tileOrder) {
    for (let i = 0; i < tileOrder.length; i++)
      tileIndex.set(tileOrder[i], i)
  }
  const sort = (xs: Tab[]) => sortTabs(xs, tileIndex)

  const ungrouped: Tab[] = []
  // Group by repo-key -> composite-branch-key. The composite key joins
  // branchName + workerId + gitToplevel so two clones of the same repo
  // (different workers OR different paths on the same worker) on the
  // same branch land in separate groups.
  const repoMap = new Map<string, {
    label: string
    branches: Map<string, { branchName: string | null, workerId: string, gitToplevel: string, isWorktree: boolean, tabs: Tab[] }>
  }>()

  // A tab belongs under Repo → Branch when we can compute a repo key from
  // its git info: an origin URL, or a toplevel. Tabs with neither (non-git
  // dirs, and tabs not yet git-stamped) stay ungrouped.
  for (const tab of tabs) {
    const rk = repoKeyAndLabel(tab, store)
    if (!rk) {
      ungrouped.push(tab)
      continue
    }
    let entry = repoMap.get(rk.key)
    if (!entry) {
      entry = { label: rk.label, branches: new Map() }
      repoMap.set(rk.key, entry)
    }
    const git = repoGitView(tab, store)
    const branchName = git.branchLabel || null
    const workerId = tab.workerId ?? ''
    const gitToplevel = tabGitToplevelForKey(tab, store, git)
    // Through the shared function, not a second copy of its body. This IS the
    // "the sidebar groups its tree by it" caller tabBranchKey's own doc describes,
    // and the composer's delete-branch dialog collects its tab list by the same
    // function -- a second membership test here would let the dialog report a
    // different set of affected tabs than the tree shows. The resolved view
    // passes in, so the loop's per-tab work is ONE resolution.
    const key = tabBranchKey(tab, store, git)
    let bucket = entry.branches.get(key)
    if (!bucket) {
      // Tabs are bucketed by (branchName, workerId, gitToplevel), so
      // every tab in a bucket shares the same worker view of the same
      // path — `isWorktree` is uniform across the bucket. Seed it
      // from the first tab; later tabs that happen to disagree (e.g.
      // a stale broadcast races a probe refresh) leave the seed as-is
      // rather than flickering the group's worktree flag.
      bucket = { branchName, workerId, gitToplevel, isWorktree: git.isWorktree ?? false, tabs: [] }
      entry.branches.set(key, bucket)
    }
    bucket.tabs.push(tab)
  }

  // Sort rule: real remotes first (alphabetical by formatted label), then
  // per-toplevel local repos (alphabetical by basename).
  const localRank = (key: string): number => isLocalRepoKey(key) ? 1 : 0

  const groups: RepoGroup[] = [...repoMap.entries()].toSorted(([aKey, a], [bKey, b]) => {
    const aRank = localRank(aKey)
    const bRank = localRank(bKey)
    if (aRank !== bRank)
      return aRank - bRank
    return a.label.localeCompare(b.label)
  }).map(([key, entry]) => {
    // First pass: count branches by name. Most branch names appear
    // exactly once (no collision) — those don't need Sets at all and
    // skip the second pass entirely. `branchNameSegment` maps the
    // `null` (no-branch) bucket to a sentinel so it never collides
    // with a real branch literally named "(no branch)".
    const nameCount = new Map<string, number>()
    for (const b of entry.branches.values()) {
      const k = branchNameSegment(b.branchName)
      nameCount.set(k, (nameCount.get(k) ?? 0) + 1)
    }
    // Second pass: allocate Sets only for collision-prone branch names.
    // Lookups against this map default to "size 1" when absent, since a
    // missing entry means the branch is unique within its repo.
    const byBranchKey = new Map<string, {
      workerIds: Set<string>
      toplevels: Set<string>
    }>()
    for (const b of entry.branches.values()) {
      const k = branchNameSegment(b.branchName)
      if ((nameCount.get(k) ?? 0) < 2)
        continue
      let stats = byBranchKey.get(k)
      if (!stats) {
        stats = { workerIds: new Set(), toplevels: new Set() }
        byBranchKey.set(k, stats)
      }
      stats.workerIds.add(b.workerId)
      stats.toplevels.add(b.gitToplevel)
    }

    // Sort: null (no branch) last, then alphabetical by branch name.
    // Within ties: worker name then toplevel path.
    const branches = [...entry.branches.values()].toSorted((a, b) => {
      if (a.branchName === null && b.branchName !== null)
        return 1
      if (a.branchName !== null && b.branchName === null)
        return -1
      if (a.branchName !== null && b.branchName !== null) {
        const c = a.branchName.localeCompare(b.branchName)
        if (c !== 0)
          return c
      }
      const aw = workerInfoFn?.(a.workerId)?.name ?? a.workerId
      const bw = workerInfoFn?.(b.workerId)?.name ?? b.workerId
      const wc = aw.localeCompare(bw)
      if (wc !== 0)
        return wc
      return a.gitToplevel.localeCompare(b.gitToplevel)
    }).map(({ branchName, workerId, gitToplevel, isWorktree, tabs: branchTabs }) => {
      // All tabs in the same branch group share the same git state, so use
      // the first tab that has diff stats rather than summing.
      let diffAdded = 0
      let diffDeleted = 0
      let diffUntracked = 0
      for (const t of branchTabs) {
        const stats = repoGitView(t, store).diffStats
        // NOTE: one resolution per tab here is the deliberate remaining cost
        // -- the bucket's tabs share worker+toplevel but not necessarily the
        // same store key (subdir agents), and diffStats are per-entry.
        if (stats.added > 0 || stats.deleted > 0 || stats.untracked > 0) {
          diffAdded = stats.added
          diffDeleted = stats.deleted
          diffUntracked = stats.untracked
          break
        }
      }
      const stats = byBranchKey.get(branchNameSegment(branchName))
      const workerCount = stats?.workerIds.size ?? 1
      // ONE lookup for the label, the tooltip's tilde path and the worker row.
      // `computeBranchDisplayLabel` used to take the lookup function and call
      // it itself, so a colliding group resolved the same worker twice.
      const info = workerInfoFn?.(workerId)
      const displayLabel = computeBranchDisplayLabel(
        branchName,
        workerId,
        gitToplevel,
        workerCount,
        stats?.toplevels.size ?? 1,
        info,
      )
      return {
        branchName,
        workerId,
        gitToplevel,
        isWorktree,
        displayLabel,
        homeDir: info?.homeDir ?? '',
        flavor: workerPathFlavor(info),
        workerLabel: workerCount > 1 ? (info?.name || workerId) : '',
        tabs: sort(branchTabs),
        diffAdded,
        diffDeleted,
        diffUntracked,
      }
    })
    let groupAdded = 0
    let groupDeleted = 0
    let groupUntracked = 0
    const branchByKey = new Map<string, BranchGroup>()
    for (const b of branches) {
      groupAdded += b.diffAdded
      groupDeleted += b.diffDeleted
      groupUntracked += b.diffUntracked
      branchByKey.set(branchKey(b.branchName, b.workerId, b.gitToplevel), b)
    }
    return {
      repoKey: key,
      repoLabel: entry.label,
      branches,
      branchByKey,
      diffAdded: groupAdded,
      diffDeleted: groupDeleted,
      diffUntracked: groupUntracked,
    }
  })

  return { groups, ungrouped: sort(ungrouped) }
}

/**
 * Build the visible branch label, appending disambiguating context only
 * when the same branch name appears in more than one group inside the
 * same repo. `workerCount` and `toplevelCount` are computed across the
 * colliding groups; their value tells us which dimensions are ambiguous
 * (and therefore should appear in the suffix).
 */
function computeBranchDisplayLabel(
  branchName: string | null,
  workerId: string,
  gitToplevel: string,
  workerCount: number,
  toplevelCount: number,
  info: WorkerInfo | null | undefined,
): string {
  const labelBase = branchName === null ? NO_BRANCH_LABEL : branchName
  if (workerCount <= 1 && toplevelCount <= 1)
    return labelBase
  const parts: string[] = []
  if (workerCount > 1) {
    const name = info?.name
    parts.push(name && name.length > 0 ? name : workerId)
  }
  if (toplevelCount > 1)
    parts.push(tildifyForWorker(gitToplevel, info))
  if (parts.length === 0)
    return labelBase
  return `${labelBase} (${parts.join(', ')})`
}

/**
 * Order tabs by their visual position in the workspace. Primary key is
 * the tab's tile in `tileIndex` (top-left tile first; the index is built
 * from `getAllTileIds(root)` upstream). Within the same tile, fall back
 * to LexoRank `position` so the sidebar tracks the tab bar's left-to-
 * right order. Tabs whose tile is absent from `tileIndex` (no `tileId`
 * yet, or a layout/snapshot race) sink to the bottom but stay grouped
 * together by tile; `id` is the final, stable tiebreak.
 *
 * When `tileIndex` is empty (no tile order supplied — e.g. a test
 * harness or a workspace whose layout hasn't been hydrated yet) every
 * tab gets the same primary rank, so the sort effectively becomes
 * position-then-id. That keeps callers without layout info from
 * producing arbitrary orderings.
 */
function sortTabs(tabs: Tab[], tileIndex: Map<string, number>): Tab[] {
  const rank = (tileId: string | undefined): number => {
    if (!tileId)
      return Number.POSITIVE_INFINITY
    const idx = tileIndex.get(tileId)
    return idx === undefined ? Number.POSITIVE_INFINITY : idx
  }
  return tabs.toSorted((a, b) => {
    const ra = rank(a.tileId)
    const rb = rank(b.tileId)
    if (ra !== rb)
      return ra - rb
    const pa = a.position ?? ''
    const pb = b.position ?? ''
    if (pa !== pb)
      return pa < pb ? -1 : 1
    return a.id.localeCompare(b.id)
  })
}
