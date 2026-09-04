import type { GitMode } from '~/hooks/useGitModeState'
import type { GitPathInfoSeed } from '~/hooks/useGitPathInfo'
import { gitModeFromToken, gitModeToken } from '~/hooks/useGitModeState'
import { assertNever } from '~/lib/assertNever'
import { localStorageGet, localStorageSet, PREFIX_WORKSPACE_GIT_MODE } from '~/lib/browserStorage'
import { basename } from '~/lib/paths'

/**
 * Where a new workspace starts FROM.
 *
 * The section header menu picks one of these and hands it to the New workspace
 * dialog, which turns it into a pre-filled form. The point of the type is that
 * the menu never re-asks what the app already knows: starting a second agent on
 * a repository the user already has open should not mean picking the worker and
 * walking the directory tree again.
 *
 * Two more variants are designed for and NOT implemented: cloning a remote
 * repository, and creating an empty one. Neither has an RPC on any worker or
 * hub service today, so both are backend work. They are named here because the
 * shape has to hold them without a redesign -- see the note on why they cannot
 * be `GitMode` radios below.
 */
export type WorkspaceStartPoint
  = | {
    /** No repository target: the user picks the directory in the dialog. */
    kind: 'directory'
    /**
     * A worker to preselect while still leaving the directory to the user.
     *
     * The `?newWorkspace=true&workerId=` route identifies a machine and nothing
     * else, which is this rather than a `repo` start point.
     */
    workerId?: string
  }
  | {
    /** A repository checkout the app has already seen this session. */
    kind: 'repo'
    workerId: string
    /**
     * The checkout's own toplevel -- the worktree root for a linked
     * worktree, NOT the main repository.
     */
    gitToplevel: string
    /**
     * Whether `gitToplevel` is a linked worktree.
     *
     * Required, and not decoration. `useChangeBranchInspect` documents the
     * failure: seeding `repoRoot` with a WORKTREE toplevel makes
     * `GitOptions.worktreePath()` suggest a new worktree nested under the
     * existing worktree's parent instead of the repository's.
     */
    isWorktree: boolean
    /** The branch the checkout is on, when the caller knows it. */
    currentBranch?: string
  }
  // FUTURE: { kind: 'clone', workerId, remoteUrl, parentDir }
  // FUTURE: { kind: 'init',  workerId, directory }
  //
  // Why neither can be a sixth `GitMode` radio, which is the obvious-looking
  // place for them: `useGitPathInfo` computes
  // `showGitOptions = isGitRepo && (isRepoRoot || isWorktreeRoot)`, and
  // `GitOptionsLoader` mounts its children only on it. A clone target does not
  // exist yet and an init target is not a repository, so both report
  // `isGitRepo=false` and `GitOptions` never renders there at all. `GitFields`
  // has no wire slot either -- it projects onto seven `OpenAgent`/`OpenTerminal`
  // proto fields and there is no `clone_url` among them. The slot each needs is
  // a new dialog COLUMN plus a submit PRELUDE that calls the future RPC and
  // yields a directory, after which the existing `openAgent` call runs
  // unchanged.

/** The `repo` variant alone, for the surfaces that only produce that one. */
export type WorkspaceRepoStartPoint = Extract<WorkspaceStartPoint, { kind: 'repo' }>

/**
 * The storage key a repository's last-used git mode is remembered under.
 *
 * `(workerId, gitToplevel)` on BOTH sides, and that is the whole contract: the
 * menu holds the pair on its start point, and the dialog holds it as
 * `worker.workingDir()` while `pathInfo.showGitOptions()` is true -- which is
 * exactly the guarantee that the path is a repository root or a worktree root.
 *
 * Do NOT key on the repo key (an origin URL) on one side and a path on the
 * other: they are different spaces and every read would miss. Do NOT key on
 * `info().repoRoot` either, which for a worktree is the main repository and
 * would merge two checkouts that deserve different answers.
 *
 * The `:` join follows `PREFIX_FILES_SORT_ORDER`, which composes the same pair.
 */
export function gitModeStickyKey(workerId: string, gitToplevel: string): string {
  return `${workerId}:${gitToplevel}`
}

/** What a start point pre-fills in the New workspace dialog. */
export interface WorkspaceStartPointSetup {
  /** Worker to preselect, or undefined to let the dialog pick. */
  workerId?: string
  /** Working directory to pre-fill, or undefined for the worker's default. */
  workingDir?: string
  /**
   * Pre-filled git-path snapshot, so `GitOptions` paints on the first render
   * instead of showing the "Loading branch info" spinner while the probe runs.
   */
  pathInfoSeed?: GitPathInfoSeed
  /** Key for the remembered git mode, or undefined when nothing is remembered. */
  stickyKey?: string
}

/**
 * Project a start point onto the dialog's pre-fill.
 *
 * There is deliberately NO `form` field and no render map. It would be
 * single-valued today, read by nobody, and a second place recording which forms
 * exist. `assertNever` already turns a new variant into a compile error, and
 * one error stops the build; the commit that adds the second form adds the
 * field with it.
 */
export function startPointDialogSetup(sp: WorkspaceStartPoint): WorkspaceStartPointSetup {
  switch (sp.kind) {
    case 'directory':
      return { workerId: sp.workerId }
    case 'repo':
      return {
        workerId: sp.workerId,
        workingDir: sp.gitToplevel,
        // The same seeding rule `useChangeBranchInspect` states: a worktree
        // toplevel is NOT the repository root, so leave `repoRoot` empty and
        // let the probe supply the authoritative one. `GitOptions` hides the
        // worktree-path preview until it lands.
        pathInfoSeed: {
          isGitRepo: true,
          isRepoRoot: !sp.isWorktree,
          isWorktreeRoot: sp.isWorktree,
          repoRoot: sp.isWorktree ? '' : sp.gitToplevel,
          repoDirName: sp.isWorktree ? '' : basename(sp.gitToplevel),
          currentBranch: sp.currentBranch ?? '',
        },
        stickyKey: gitModeStickyKey(sp.workerId, sp.gitToplevel),
      }
    default:
      return assertNever(sp)
  }
}

/**
 * The git mode this repository was last started with, or undefined when
 * nothing is remembered (or what is remembered no longer identifies a mode).
 *
 * The menu shows it as the row's detail, and the dialog opens on it. Both read
 * THIS function, so the row cannot promise a mode the dialog then does not
 * select.
 */
export function readStickyGitMode(stickyKey: string | undefined): GitMode | undefined {
  if (!stickyKey)
    return undefined
  return gitModeFromToken(localStorageGet<string>(`${PREFIX_WORKSPACE_GIT_MODE}${stickyKey}`))
}

/**
 * Remember `mode` for this repository.
 *
 * Called on a SUCCESSFUL submit only: a mode the user selected and then
 * abandoned is not what they work with, and remembering it would seed the next
 * dialog from a decision that was cancelled.
 */
export function rememberStickyGitMode(stickyKey: string | undefined, mode: GitMode): void {
  if (!stickyKey)
    return
  localStorageSet(`${PREFIX_WORKSPACE_GIT_MODE}${stickyKey}`, gitModeToken(mode))
}
