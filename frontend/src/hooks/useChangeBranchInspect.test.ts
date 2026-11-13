/// <reference types="vitest/globals" />
import type { InspectBranchChangeResponse } from '~/generated/proto/leapmux/v1/git_pb'
import { createRoot, onCleanup } from 'solid-js'
import { beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { workerGit } from '~/api/workerRpc'
import { useChangeBranchInspect } from '~/hooks/useChangeBranchInspect'

vi.mock('~/api/workerRpc', () => ({
  workerGit: {
    inspectBranchChange: vi.fn(),
  },
}))

function makeResp(overrides: Partial<InspectBranchChangeResponse> = {}): InspectBranchChangeResponse {
  return {
    $typeName: 'leapmux.v1.InspectBranchChangeResponse',
    repoRoot: '/repo',
    toplevel: '/repo',
    isWorktree: false,
    currentBranch: 'main',
    attachedBranch: overrides.currentBranch ?? 'main',
    branches: [],
    ...overrides,
  } as InspectBranchChangeResponse
}

async function flushMicrotasks() {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

describe('useChangeBranchInspect', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('seeds gitInfo from props.branchName synchronously (showGitOptions=true on first read)', async () => {
    // Held pending so the test can observe the pre-RPC seed state.
    vi.mocked(workerGit.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'feature',
      })
      // Seed makes showGitOptions truthy before the RPC resolves — the
      // dialog reads this to gate form rendering.
      expect(inspect.pathInfo.showGitOptions()).toBe(true)
      expect(inspect.pathInfo.info().currentBranch).toBe('feature')
      expect(inspect.pathInfo.info().repoRoot).toBe('/repo')
      // Without an isWorktree hint, the seed assumes main-repo shape
      // (the dominant case). Worktree callers thread the hint to keep
      // the seed honest — see the next test.
      expect(inspect.pathInfo.info().isRepoRoot).toBe(true)
      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(false)
      // No probe has resolved yet, so branches() is still null and
      // branchesLoading() is true (the only way BranchSelect knows to
      // render its loading placeholder for the preloaded path).
      expect(inspect.branches()).toBeNull()
      expect(inspect.branchesLoading()).toBe(true)
    })
  })

  it('seeds isWorktreeRoot=true / isRepoRoot=false when the caller threads isWorktree=true', async () => {
    // Regression: the seed used to hard-code isRepoRoot=true /
    // isWorktreeRoot=false even when the dialog was opened from a
    // worktree row, so any GitOptions memo branching on the seed (e.g.
    // the suggested worktree-path computation) computed against the
    // wrong shape until the inspect RPC corrected it. The hook now
    // accepts an `isWorktree` arg that drives the seed.
    vi.mocked(workerGit.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo/wt-foo',
        branchName: 'feature',
        isWorktree: true,
      })
      expect(inspect.pathInfo.info().isRepoRoot).toBe(false)
      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(true)
      // showGitOptions still truthy (either root flag satisfies the
      // gate); the worktree flavour just paints the worktree affordances
      // instead of the main-repo ones.
      expect(inspect.pathInfo.showGitOptions()).toBe(true)
    })
  })

  it('does NOT seed repoRoot from gitToplevel when isWorktree=true', async () => {
    // Regression: an earlier revision seeded `repoRoot: args.gitToplevel`
    // unconditionally. For a worktree-opened dialog, `gitToplevel` IS
    // the worktree root (not the main repo), so GitOptions.worktreePath()
    // — which reads `i.repoRoot` and builds `<parent>/<repoDirName>-
    // worktrees/<branch>` — computed a path nested under the existing
    // worktree's parent. The fix leaves repoRoot empty until the inspect
    // RPC returns the authoritative main-repo root; the worktree-path
    // preview is gated on `!i.repoRoot` and hides until the value lands.
    vi.mocked(workerGit.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repos/main/worktrees/feat-x',
        branchName: 'feat-x',
        isWorktree: true,
      })
      expect(inspect.pathInfo.info().repoRoot).toBe('')
      expect(inspect.pathInfo.info().repoDirName).toBe('')
      // Non-worktree dialogs still seed repoRoot synchronously — verify
      // the carve-out is scoped to the worktree variant.
    })

    vi.mocked(workerGit.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repos/main',
        branchName: 'main',
      })
      expect(inspect.pathInfo.info().repoRoot).toBe('/repos/main')
      expect(inspect.pathInfo.info().repoDirName).toBe('main')
    })
  })

  it('suppresses pathInfo.loading through skipLoadingFlash while the seed paints the form', async () => {
    // The seed makes showGitOptions truthy; the createGuardedFetch
    // skipLoadingFlash predicate reads that, so the path-info loading
    // flag stays false during the in-flight RPC. Without this, the
    // GitOptionsLoader would flash its spinner over the already-painted
    // form on every dialog open.
    vi.mocked(workerGit.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'feature',
      })
      await flushMicrotasks()
      expect(inspect.pathInfo.loading()).toBe(false)
    })
  })

  it('refines isWorktree and branches once the RPC resolves', async () => {
    vi.mocked(workerGit.inspectBranchChange).mockResolvedValue(makeResp({
      isWorktree: true,
      branches: [
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'main', isRemote: false },
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'feature', isRemote: false },
      ],
    }))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      await flushMicrotasks()
      // isWorktree response means the path resolves to a worktree root,
      // NOT a main repo root. The hook projects this onto isRepoRoot /
      // isWorktreeRoot so GitOptions can render the "Currently in
      // worktree" affordance.
      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(true)
      expect(inspect.pathInfo.info().isRepoRoot).toBe(false)
      expect(inspect.branches()).toHaveLength(2)
      expect(inspect.branchesLoading()).toBe(false)
    })
  })

  // The worker id routes the call and stays out of the request body.
  it('sends the toplevel to the worker that owns it, and nothing else', async () => {
    vi.mocked(workerGit.inspectBranchChange).mockResolvedValue(makeResp())
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      await flushMicrotasks()
      expect(workerGit.inspectBranchChange).toHaveBeenCalledWith('w1', { path: '/repo' }, {
        signal: expect.any(AbortSignal),
      })
    })
  })

  it('refresh() re-issues the RPC', async () => {
    vi.mocked(workerGit.inspectBranchChange).mockResolvedValue(makeResp())
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      await flushMicrotasks()
      expect(workerGit.inspectBranchChange).toHaveBeenCalledTimes(1)
      inspect.refresh()
      await flushMicrotasks()
      expect(workerGit.inspectBranchChange).toHaveBeenCalledTimes(2)
    })
  })

  it('forwards RPC failures to onError', async () => {
    const onError = vi.fn()
    vi.mocked(workerGit.inspectBranchChange).mockRejectedValue(new Error('boom'))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
        onError,
      })
      await flushMicrotasks()
      expect(onError).toHaveBeenCalledWith(expect.any(Error))
    })
  })

  it('hides GitOptions on RPC failure but keeps the caller seed', async () => {
    // Regression: the seed paints isGitRepo=true / isRepoRoot=true based
    // on the caller's optimistic belief that the row's gitToplevel is
    // still a git repo. If the worker rejects with "not a git repository"
    // (rm -rf .git, mount loss, permission flip), the dialog was stacking
    // branch radios on top of the error banner because info() still held
    // the seed. `isGitRepo` goes false, so showGitOptions flips false and
    // GitOptionsLoader hides the form.
    //
    // The KIND and the BRANCH LABEL survive, because they come from the row
    // that opened the dialog and a failed RPC disproves neither. The dialog's
    // header renders ABOVE GitOptionsLoader, so clearing them relabelled a
    // worktree "Branch" with a blank name beside the error banner.
    vi.mocked(workerGit.inspectBranchChange).mockRejectedValue(new Error('not a git repository'))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      // Pre-flush: seed is live, form would render.
      expect(inspect.pathInfo.showGitOptions()).toBe(true)
      await flushMicrotasks()
      // The form hides, and the BranchSelect loading flag clears so the
      // dialog isn't stuck on a spinner.
      expect(inspect.pathInfo.info().isGitRepo).toBe(false)
      expect(inspect.pathInfo.showGitOptions()).toBe(false)
      expect(inspect.branchesLoading()).toBe(false)
      // The worker's own answers are gone; the row's are not.
      expect(inspect.pathInfo.info().repoRoot).toBe('')
      expect(inspect.pathInfo.info().isRepoRoot).toBe(true)
      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(false)
      expect(inspect.pathInfo.info().currentBranch).toBe('main')
    })
  })

  it('keeps a worktree seed on RPC failure', async () => {
    vi.mocked(workerGit.inspectBranchChange).mockRejectedValue(new Error('worker offline'))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo-worktrees/feature',
        branchName: 'feature',
        isWorktree: true,
      })
      await flushMicrotasks()

      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(true)
      expect(inspect.pathInfo.info().isRepoRoot).toBe(false)
      expect(inspect.pathInfo.info().currentBranch).toBe('feature')
      expect(inspect.pathInfo.showGitOptions()).toBe(false)
    })
  })

  it('refuses to RPC with an empty gitToplevel (defense against unstamped branch rows)', async () => {
    // Mirror of useDeleteBranchInspect: an unstamped tab whose
    // gitToplevel hasn't been resolved would otherwise ship path='' to
    // the worker, which SanitizePath rejects as permission-denied. The
    // hook short-circuits and clears branchesLoading so the dialog
    // doesn't sit spinning on a doomed RPC.
    const onError = vi.fn()
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '',
        branchName: 'main',
        onError,
      })
      await flushMicrotasks()
      expect(workerGit.inspectBranchChange).not.toHaveBeenCalled()
      expect(onError).toHaveBeenCalledTimes(1)
      expect((onError.mock.calls[0]?.[0] as Error).message).toMatch(/no resolved repo path/)
      expect(inspect.branchesLoading()).toBe(false)
    })
  })

  it.each(['before await', 'after await'] as const)('propagates a failure %s and disposes the pending inspection', async (boundary) => {
    let signal: AbortSignal | undefined
    const cleanup = vi.fn()
    const aborted = vi.fn()
    const failure = new Error('intentional root failure')
    onTestFinished(() => {
      expect(signal?.aborted).toBe(true)
      expect(aborted).toHaveBeenCalledTimes(1)
      expect(cleanup).toHaveBeenCalledTimes(1)
    })
    vi.mocked(workerGit.inspectBranchChange).mockImplementation((_workerId, _request, options) => {
      signal = options?.signal
      return new Promise((_resolve, reject) => {
        signal?.addEventListener('abort', () => {
          aborted()
          reject(signal?.reason)
        }, { once: true })
      })
    })
    await expect(createRoot(async (dispose) => {
      onTestFinished(dispose)
      onCleanup(cleanup)
      useChangeBranchInspect({ workerId: 'w1', gitToplevel: '/repo', branchName: 'main' })
      if (boundary === 'after await')
        await flushMicrotasks()
      throw failure
    })).rejects.toBe(failure)
    expect(signal).toBeInstanceOf(AbortSignal)
    expect(signal?.aborted).toBe(false)
  })
})

describe('useChangeBranchInspect attached branch identity', () => {
  it('keeps a detached response separate from the sidebar display seed', async () => {
    vi.mocked(workerGit.inspectBranchChange).mockResolvedValueOnce(makeResp({ currentBranch: 'a1b2c3d', attachedBranch: '' }))
    await createRoot(async (dispose) => {
      onTestFinished(dispose)
      const inspect = useChangeBranchInspect({ workerId: 'w1', gitToplevel: '/repo', branchName: 'a1b2c3d' })
      expect(inspect.pathInfo.info().attachedBranch).toBe('')
      await flushMicrotasks()
      expect(inspect.pathInfo.info().currentBranch).toBe('a1b2c3d')
      expect(inspect.pathInfo.info().attachedBranch).toBe('')
    })
  })
})
