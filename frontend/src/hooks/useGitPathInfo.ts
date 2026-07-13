import type { Accessor } from 'solid-js'
import { createEffect, createMemo, createSignal, on } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { useOrg } from '~/context/OrgContext'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { createLogger } from '~/lib/logger'
import { shallowEqual, shallowEqualArrays } from '~/lib/shallowEqual'

const log = createLogger('useGitPathInfo')

// Module-scoped seed for the probeKey memo. Static so the createMemo
// init slot doesn't end up evaluating an expression that reads the
// (workerId, path, orgId) accessors in the parent's reactive scope.
const EMPTY_PROBE_KEY: readonly [string, string, string] = ['', '', '']

export interface UseGitPathInfoOptions {
  /**
   * Opt-in: invoked once with the canonical repo root the first time the
   * probe resolves to a worktree root. Lets callers reroute their
   * `path` signal from a worktree-root default to the parent repo so
   * GitOptions presents branch operations against the repo. The hook
   * never writes back into the path signal it reads from, so callers
   * own the setter.
   */
  remapWorktreeRoot?: (repoRoot: string) => void
  /**
   * Pre-fill the snapshot with values the caller already knows so
   * GitOptionsLoader can render children synchronously instead of
   * blocking on the spinner. The probe still runs in the background to
   * refine volatile fields (currentBranch, isDirty) and to correct any
   * seeded fields the caller guessed wrong. Typical use: a dialog that
   * was opened against a known repo root.
   */
  seed?: GitPathInfoSeed
}

export type GitPathInfoSeed = Partial<GitInfoFields>

export interface GitInfoFields {
  isGitRepo: boolean
  isRepoRoot: boolean
  isWorktreeRoot: boolean
  isDirty: boolean
  repoRoot: string
  repoDirName: string
  currentBranch: string
  /**
   * Non-empty when the worker could not classify the path as a git
   * repo for a non-"not a repo" reason (dubious-ownership, EACCES,
   * transient I/O). Carries git's stderr verbatim so the dialog can
   * render an inline diagnostic instead of the previous opaque
   * "Internal error" toast. Empty when isGitRepo=true or when the
   * path is genuinely not a repo. Mirrors
   * GetGitInfoResponse.error_hint and
   * GetGitFileStatusResponse.error_hint on the wire.
   */
  errorHint: string
}

export const EMPTY_INFO: GitInfoFields = {
  isGitRepo: false,
  isRepoRoot: false,
  isWorktreeRoot: false,
  isDirty: false,
  repoRoot: '',
  repoDirName: '',
  currentBranch: '',
  errorHint: '',
}

// Single source of truth for "construct a GitInfoFields from an optional
// seed". The seed overlays EMPTY_INFO so a caller that only knows
// `repoRoot` doesn't have to spell out every other field. Returning the
// `EMPTY_INFO` constant when no seed is passed lets `resetToEmpty`'s
// shallow-equality short-circuit work without allocating. Exported so
// useChangeBranchInspect / useDeleteBranchInspect can build their own
// seeded snapshots through the same constructor — keeps every
// GitInfoFields literal in the codebase routed through this helper, so
// adding a new field (e.g. errorHint) lands at one site rather than
// drifting across each sibling inspect hook.
export function buildInfo(seed?: GitPathInfoSeed): GitInfoFields {
  if (!seed)
    return EMPTY_INFO
  return { ...EMPTY_INFO, ...seed }
}

export interface GitPathInfo {
  loading: Accessor<boolean>
  /** Full snapshot of the latest probe (or seeded values pre-probe). */
  info: Accessor<GitInfoFields>
  /** True iff the path resolves to a git repo root or a worktree root. */
  showGitOptions: Accessor<boolean>
}

/**
 * Wraps the workerRpc.getGitInfo probe behind a stable reactive surface
 * so consumers can drive their own loading/visibility UX. The probe
 * fires whenever workerId/path change (independent of whether
 * GitOptions is mounted), so a consumer can render a spinner during
 * `loading()` and mount GitOptions only when `showGitOptions()` flips
 * true.
 */
export function useGitPathInfo(
  workerId: Accessor<string>,
  path: Accessor<string>,
  options: UseGitPathInfoOptions = {},
): GitPathInfo {
  const org = useOrg()
  // One signal holding the full record. Consumers read fields via
  // `info().X`, so SolidJS only triggers a single update when the probe
  // lands (no batch() ceremony, no setter fan-out).
  const [info, setInfo] = createSignal<GitInfoFields>(buildInfo(options.seed))
  // One-shot gate for `remapWorktreeRoot`, scoped per (workerId, path)
  // pair. A plain boolean since callers don't observe it.
  //
  // Reset when:
  //   - workerId/path cycle to empty (clearing and re-entering counts as
  //     a fresh first probe — see `fetch()` below);
  //   - workerId changes to a different non-empty value (a worker swap
  //     in useWorkerDialog-wrapped dialogs may resolve the same path to
  //     a brand-new worktree root, and the new worker deserves its own
  //     remap-on-first-probe).
  //
  // A path change on the SAME worker leaves the flag set: the user is
  // navigating around within the same worker's filesystem, and re-
  // remapping their explicit subdir-pick back to the repo root every
  // time would yank focus out of their input.
  let firstProbeSeen = false
  let firstProbeWorkerId = ''

  // Memo so downstream `on()` effects don't refire when info() updates
  // without changing the boolean (e.g. the post-seed probe lands and
  // fills in currentBranch but showGitOptions stays true).
  const showGitOptions = createMemo(() => {
    const i = info()
    return i.isGitRepo && (i.isRepoRoot || i.isWorktreeRoot)
  })

  // Shared between the workerId/path-clear early return and the RPC
  // onError path. The shallowEqual gate keeps every reset path from
  // firing a no-op setInfo notification when the snapshot is already
  // empty (e.g. an error after a prior clear).
  const resetToEmpty = () => {
    if (!shallowEqual(info(), EMPTY_INFO))
      setInfo(buildInfo())
  }

  const fetcher = createGuardedFetch<{ wid: string, p: string }, Awaited<ReturnType<typeof workerRpc.getGitInfo>>>({
    fetch: ({ wid, p }, signal) => workerRpc.getGitInfo(wid, { workerId: wid, path: p, orgId: org.orgId() }, { signal }),
    applySuccess: (resp, args) => {
      // First-probe-on-worktree-root → remap: the caller's path setter
      // will switch to the canonical repoRoot, retriggering the probe.
      // Skip the setInfo here so the dialog never paints worktree-scoped
      // currentBranch/isDirty before the repoRoot's probe lands.
      const willRemap = !firstProbeSeen
        && resp.isWorktreeRoot
        && !!resp.repoRoot
        && !!options.remapWorktreeRoot
      if (!willRemap) {
        const next: GitInfoFields = {
          isGitRepo: resp.isGitRepo,
          isRepoRoot: resp.isRepoRoot,
          isWorktreeRoot: resp.isWorktreeRoot,
          isDirty: resp.isDirty,
          repoRoot: resp.repoRoot,
          repoDirName: resp.repoDirName,
          currentBranch: resp.currentBranch,
          errorHint: resp.errorHint,
        }
        // Skip the setInfo when the probe payload is identical to what's
        // already in the signal — every setInfo replaces the value by
        // identity and refires every downstream accessor, even when
        // nothing actually changed (e.g. a refresh() after a no-op
        // operation, or a path/wid change that lands on the same repo).
        if (!shallowEqual(info(), next))
          setInfo(next)
      }
      if (!firstProbeSeen) {
        firstProbeSeen = true
        firstProbeWorkerId = args.wid
        if (resp.isWorktreeRoot && resp.repoRoot)
          options.remapWorktreeRoot?.(resp.repoRoot)
      }
    },
    onError: (err, args) => {
      log.warn('Failed to get git info', err)
      resetToEmpty()
      // Flip the first-probe gate on failure too — the doc-comment on
      // `firstProbeSeen` calls for one remap "on first encounter," not
      // "first SUCCESSFUL encounter." If the first attempt fails (ctx
      // cancel, transient network) and the user manually navigates to
      // a canonical path before the next probe lands, we shouldn't
      // remap them out of their explicit choice.
      firstProbeSeen = true
      firstProbeWorkerId = args.wid
    },
    // Skip the spinner when transitioning between known git repos — the
    // previous data is good enough as a placeholder until the new probe
    // completes, and the flash hurts the dialog more than a brief stale
    // value does.
    skipLoadingFlash: showGitOptions,
  })

  const fetch = async () => {
    const wid = workerId()
    const p = path()
    if (!wid || !p) {
      resetToEmpty()
      firstProbeSeen = false
      firstProbeWorkerId = ''
      await fetcher.run(null)
      return
    }
    // Worker swap: a non-empty workerId change resolves the same path
    // against a brand-new worker. The new worker may resolve the path
    // to a worktree root that the old worker didn't, and the user has
    // NOT manually adjusted the path (they swapped workers, not paths)
    // — so reset the remap gate. A path change on the same worker
    // keeps the flag (see the field's doc above).
    if (firstProbeSeen && firstProbeWorkerId !== wid) {
      firstProbeSeen = false
    }
    await fetcher.run({ wid, p })
  }

  // Track orgId alongside workerId/path: the fetch closure reads orgId
  // out of the org context, so an orgId change without a workerId/path
  // change must still refire the probe (otherwise the stale orgId is
  // baked into the RPC). Stamp the tuple through a memo with
  // value-comparing equality so identity churn upstream (e.g. the auth
  // user re-loading to the same value) doesn't refire the probe.
  //
  // The seed is a STATIC empty tuple, not another `[workerId(), path(),
  // org.orgId()]` expression: Solid's createMemo second arg is the
  // initial value, NOT a second computation. Passing the live tuple
  // there would evaluate the three accessors in the hook's reactive
  // scope (not inside the memo), wasting work AND creating signal
  // subscriptions on the parent owner that the memo's own equals
  // callback can't dedupe.
  const probeKey = createMemo<readonly [string, string, string]>(
    () => [workerId(), path(), org.orgId()] as const,
    EMPTY_PROBE_KEY,
    { equals: shallowEqualArrays },
  )
  createEffect(on(probeKey, () => {
    void fetch()
  }))

  return {
    loading: fetcher.loading,
    info,
    showGitOptions,
  }
}
