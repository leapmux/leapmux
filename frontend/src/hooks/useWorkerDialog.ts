import type { WorkerDialogContextOptions } from '~/hooks/createWorkerDialogContext'
import type { UseDialogSubmitOptions } from '~/hooks/useDialogSubmit'
import type { GitModeIntent } from '~/hooks/useGitModeState'
import type { GitPathInfoSeed } from '~/hooks/useGitPathInfo'
import { createWorkerDialogContext } from '~/hooks/createWorkerDialogContext'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { useGitModeState } from '~/hooks/useGitModeState'
import { useGitPathInfo } from '~/hooks/useGitPathInfo'

/**
 * Path-info options exposed by useWorkerDialog. Modeled as a
 * discriminated union so the two mutually-exclusive setup modes can't
 * accidentally coexist:
 *
 *  - `{ seed }` — the caller already knows the path is a repo/worktree
 *    root and pre-fills the snapshot for a flicker-free first paint.
 *    The probe still runs to refine volatile fields.
 *  - `{ remapWorktreeRoot: true }` — the caller's path may resolve to a
 *    worktree root the canonical repoRoot of which is unknown until the
 *    probe lands; useWorkerDialog wires the worker's setWorkingDir into
 *    useGitPathInfo so the first probe reroutes the dialog to the
 *    canonical repo.
 *
 * Combining the two would race: `seed` makes
 * useGitPathInfo.showGitOptions flip true synchronously, and GitOptions
 * paints against the seed's worktree path BEFORE the remap fires. The
 * subsequent CheckoutBranch / CreateBranch RPC would land against the
 * worktree dir instead of the canonical repoRoot, silently moving the
 * worktree's HEAD instead of the main repo's. The union forbids that
 * combination at the type level — `seed` and `remapWorktreeRoot: true`
 * cannot coexist in a single options literal.
 */
export type WorkerDialogPathInfoOptions
  = | { seed?: GitPathInfoSeed, remapWorktreeRoot?: false }
    | {
      /**
       * When true, the path-info probe's first hit against a worktree
       * root reroutes the working dir to the canonical repo. Mutually
       * exclusive with `seed` — see the union doc above.
       */
      remapWorktreeRoot: true
      seed?: never
    }

export interface WorkerDialogGitModeOptions {
  /**
   * Initial GitModeIntent. Pass for dialogs that open on a non-default
   * mode (e.g. ChangeBranchDialog defaults to `SwitchBranch`) so the
   * radio paints correctly on first render.
   */
  initialIntent?: GitModeIntent
}

export interface UseWorkerDialogOptions {
  /** Options for the `useDialogSubmit` sub-hook (fallback, formatError, timeoutMs). */
  submit?: UseDialogSubmitOptions
  /**
   * Options for the `createWorkerDialogContext` sub-hook
   * (preselectedWorkerId, defaultWorkingDir, singleWorkerId). `onError`
   * is wired internally to `submit.setError` so the dialog shows
   * fleet-listing failures in the same slot as submit failures.
   */
  worker?: Omit<WorkerDialogContextOptions, 'onError'>
  /** Options for the `useGitModeState` sub-hook. */
  gitMode?: WorkerDialogGitModeOptions
  /** Options for the `useGitPathInfo` sub-hook. */
  pathInfo?: WorkerDialogPathInfoOptions
}

/**
 * Composes the four scaffolding hooks every worker-targeted dialog
 * needs — submit lifecycle, worker/dir selection, git-mode intent, and
 * the git-path probe. Each concern is returned as a sibling so callers
 * read `worker.workerId()` / `gitMode.currentIntent()` / `pathInfo.info()`
 * without spreading sub-hook fields into one ambiguous bag.
 *
 * Tree state (`createDirectoryTreeState`), provider selection
 * (`useAgentProviderSelection`) and shell selection (`useAvailableShells`)
 * stay separate because not every dialog uses them.
 */
export function useWorkerDialog(options: UseWorkerDialogOptions = {}) {
  const submit = useDialogSubmit(options.submit)
  const worker = createWorkerDialogContext({
    ...options.worker,
    onError: submit.setError,
  })
  const gitMode = useGitModeState(options.gitMode?.initialIntent)
  // The union narrows so that exactly one of `seed` / `remapWorktreeRoot`
  // is present per option literal; reading both off the union is safe
  // because the type system guarantees the unused branch is undefined.
  const pi = options.pathInfo
  const pathInfo = useGitPathInfo(worker.workerId, worker.workingDir, {
    remapWorktreeRoot: pi && 'remapWorktreeRoot' in pi && pi.remapWorktreeRoot === true
      ? worker.setWorkingDir
      : undefined,
    seed: pi && 'seed' in pi ? pi.seed : undefined,
  })
  return { submit, worker, gitMode, pathInfo }
}

export type WorkerDialog = ReturnType<typeof useWorkerDialog>
