import type { Accessor } from 'solid-js'
import type { GitBranchEntry, InspectBranchChangeResponse } from '~/generated/leapmux/v1/git_pb'
import type { GitInfoFields, GitPathInfo } from '~/hooks/useGitPathInfo'
import { createMemo, createSignal, onMount } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { useOrg } from '~/context/OrgContext'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { buildInfo, EMPTY_INFO } from '~/hooks/useGitPathInfo'
import { createLogger } from '~/lib/logger'
import { basename } from '~/lib/paths'

const log = createLogger('useChangeBranchInspect')

export interface UseChangeBranchInspectArgs {
  workerId: string
  gitToplevel: string
  /** Branch label the calling row already knows; used to seed the pre-RPC paint. */
  branchName: string | null
  /**
   * Worktree disposition the calling row already knows (from the
   * sidebar's BranchGroup.isWorktree). Seeds isRepoRoot/isWorktreeRoot
   * correctly before the inspect RPC lands; defaults to false (main
   * repo) when omitted for call sites that don't yet thread it.
   */
  isWorktree?: boolean
  /** Optional error sink for the dialog's banner. */
  onError?: (err: unknown) => void
}

export interface ChangeBranchInspect {
  /**
   * GitPathInfo-shaped accessor synthesized from the InspectBranchChange
   * response. Plugs into GitOptions exactly like the result of
   * `useGitPathInfo` would, but never issues a separate `GetGitInfo`
   * probe — the path-info fields come from the same bundle RPC that
   * supplied the branches list.
   *
   * `pathInfo.loading` is suppressed by `skipLoadingFlash` once the
   * seed makes `showGitOptions` truthy, so the dialog's
   * GitOptionsLoader paints the form on the first frame instead of
   * flashing the spinner. Use `branchesLoading` below for the
   * BranchSelect's own loading state, which is independent.
   */
  pathInfo: GitPathInfo
  /**
   * Branches list returned by InspectBranchChange. `null` while the
   * RPC is in flight (no response yet); becomes an array on resolution.
   * Plugs into GitOptions' `preloadedBranches` prop.
   */
  branches: Accessor<GitBranchEntry[] | null>
  /**
   * True while an inspect RPC is in flight. Tracks every fetch — initial
   * mount AND user-triggered refresh — so the BranchSelect can render a
   * spinner during refresh instead of showing stale options. Distinct
   * from `pathInfo.loading`, which is suppressed by skipLoadingFlash.
   */
  branchesLoading: Accessor<boolean>
  /**
   * Refire the bundle RPC. Returns a Promise that resolves when the
   * fetch settles (success OR error) so callers can `await` it before
   * triggering follow-up UI. Used by GitOptions' Refresh button.
   */
  refresh: () => Promise<void>
}

/**
 * One-shot bundle probe for ChangeBranchDialog. Issues a single
 * `InspectBranchChange` RPC and projects the response onto the same
 * GitPathInfo shape that `useGitPathInfo` exposes, so GitOptions can
 * consume it unchanged. Drops the previous two-RPC sequence
 * (`GetGitInfo` then `ListGitBranches`) down to one.
 *
 * The dialog seeds the GitInfoFields with what the calling row already
 * knows (repoRoot, currentBranch) so the first paint is correct
 * pre-RPC; the response then refines `isDirty`, `isWorktree`, and the
 * branch list in one update.
 */
export function useChangeBranchInspect(args: UseChangeBranchInspectArgs): ChangeBranchInspect {
  const org = useOrg()

  // Seed the synthesized path-info snapshot from the calling row.
  // ChangeBranchDialog is opened against a known toplevel, so we can
  // paint Switch/Create/Worktree options without waiting for the RPC.
  // Use the caller's worktree hint to seed isRepoRoot / isWorktreeRoot;
  // otherwise a worktree-opened dialog briefly claims it's at the main
  // repo root and any GitOptions memo branching on the seed (e.g. the
  // suggested-worktree-path computation reading repoRoot) computes
  // against the wrong shape until the inspect RPC lands.
  //
  // For worktree-opened dialogs, `args.gitToplevel` is the worktree
  // root, NOT the main repo root — seeding `repoRoot` with it would
  // make `worktreePath()` compute a suggested new-worktree path nested
  // under the existing worktree's parent. Leave `repoRoot` empty so
  // GitOptions's `if (!i.repoRoot)` guard hides the preview until the
  // inspect RPC returns the authoritative main-repo root.
  const seedIsWorktree = args.isWorktree ?? false
  // buildInfo() overlays EMPTY_INFO under the hood so new fields added
  // to GitInfoFields (e.g. errorHint) auto-default without a parallel
  // edit here.
  const seedInfo = buildInfo({
    isGitRepo: true,
    isRepoRoot: !seedIsWorktree,
    isWorktreeRoot: seedIsWorktree,
    repoRoot: seedIsWorktree ? '' : args.gitToplevel,
    repoDirName: seedIsWorktree ? '' : basename(args.gitToplevel),
    currentBranch: args.branchName ?? '',
  })
  const [info, setInfo] = createSignal<GitInfoFields>(seedInfo)
  const [branches, setBranches] = createSignal<GitBranchEntry[] | null>(null)
  // Separate "branches RPC in flight" signal. We can't reuse
  // fetcher.loading: the inspect bundle is shared with pathInfo, whose
  // loading is suppressed by skipLoadingFlash so the form stays
  // interactive during a refresh. The BranchSelect's "Loading
  // branches..." UI is a different concern — it must reflect EVERY
  // RPC, including refresh, otherwise stale options sit on screen for
  // the whole refresh latency with no indication that anything is in
  // flight.
  const [branchesLoading, setBranchesLoading] = createSignal(true)

  const showGitOptions = createMemo(() => {
    const i = info()
    return i.isGitRepo && (i.isRepoRoot || i.isWorktreeRoot)
  })

  const fetcher = createGuardedFetch<void, InspectBranchChangeResponse>({
    fetch: (_args, signal) => workerRpc.inspectBranchChange(args.workerId, {
      orgId: org.orgId(),
      workerId: args.workerId,
      path: args.gitToplevel,
    }, { signal }),
    // Suppress the loading flag while the seed already paints the form
    // (every dialog opens against a known repo root). This matches
    // useGitPathInfo's behaviour for seeded callers and keeps the form
    // interactive while the inspect RPC is in flight.
    skipLoadingFlash: showGitOptions,
    applySuccess: (resp) => {
      // Project onto the GitInfoFields shape so GitOptions reads it
      // through the same accessor as the useGitPathInfo path. We don't
      // know `isRepoRoot` vs. `isWorktreeRoot` from the bundle directly,
      // so derive: a worktree response means the path is a worktree
      // root; otherwise it's the repo root.
      setInfo(buildInfo({
        isGitRepo: true,
        isRepoRoot: !resp.isWorktree,
        isWorktreeRoot: resp.isWorktree,
        isDirty: resp.isDirty,
        repoRoot: resp.repoRoot,
        repoDirName: basename(resp.repoRoot),
        currentBranch: resp.currentBranch,
      }))
      setBranches(resp.branches)
      setBranchesLoading(false)
    },
    onError: (err) => {
      log.warn('inspectBranchChange failed', err)
      // Reset info to a non-repo shape so GitOptionsLoader hides
      // GitOptions on failure. The seed paints isGitRepo=true based on
      // the calling row's optimistic belief; if inspect tells us the
      // path is no longer a git repo (rm -rf .git, mount loss, worker
      // permission flip), keeping the seed would stack branch radios on
      // top of an error banner for a destination the worker rejected.
      // Routing through EMPTY_INFO (the single source of truth from
      // useGitPathInfo) means a new GitInfoFields field added later
      // automatically lands here at its zero value — no parallel edit
      // to keep in sync with applySuccess.
      setInfo(EMPTY_INFO)
      args.onError?.(err)
      setBranchesLoading(false)
    },
  })

  const runFetch = () => {
    // Defense-in-depth: never ship `path: ""` to the worker — its
    // SanitizePath would reject the request as permission denied, which
    // surfaces in the dialog banner as a cryptic auth-style error.
    // WorkspaceTabTree already hides the menu when the row has no
    // gitToplevel, so this gate only catches a programming mistake.
    if (!args.gitToplevel) {
      const err = new Error('Cannot inspect branch change: tab has no resolved repo path yet')
      log.warn('inspectBranchChange skipped', err)
      args.onError?.(err)
      setBranchesLoading(false)
      return Promise.resolve()
    }
    // Flip branchesLoading BEFORE the fetcher's batch runs so the
    // BranchSelect placeholder paints immediately. applySuccess /
    // onError clear it once the RPC settles.
    setBranchesLoading(true)
    return fetcher.run()
  }

  onMount(() => {
    void runFetch()
  })

  const pathInfo: GitPathInfo = {
    loading: fetcher.loading,
    info,
    showGitOptions,
  }

  return {
    pathInfo,
    branches,
    branchesLoading,
    refresh: runFetch,
  }
}
