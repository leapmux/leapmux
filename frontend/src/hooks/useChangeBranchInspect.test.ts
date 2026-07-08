/// <reference types="vitest/globals" />
import type { InspectBranchChangeResponse } from '~/generated/leapmux/v1/git_pb'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { useChangeBranchInspect } from '~/hooks/useChangeBranchInspect'

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/workerRpc', () => ({
  inspectBranchChange: vi.fn(),
}))

function makeResp(overrides: Partial<InspectBranchChangeResponse> = {}): InspectBranchChangeResponse {
  return {
    $typeName: 'leapmux.v1.InspectBranchChangeResponse',
    repoRoot: '/repo',
    toplevel: '/repo',
    isWorktree: false,
    currentBranch: 'main',
    isDirty: false,
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
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
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
      dispose()
    })
  })

  it('seeds isWorktreeRoot=true / isRepoRoot=false when the caller threads isWorktree=true', async () => {
    // Regression: the seed used to hard-code isRepoRoot=true /
    // isWorktreeRoot=false even when the dialog was opened from a
    // worktree row, so any GitOptions memo branching on the seed (e.g.
    // the suggested worktree-path computation) computed against the
    // wrong shape until the inspect RPC corrected it. The hook now
    // accepts an `isWorktree` arg that drives the seed.
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
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
      dispose()
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
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
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
      dispose()
    })

    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repos/main',
        branchName: 'main',
      })
      expect(inspect.pathInfo.info().repoRoot).toBe('/repos/main')
      expect(inspect.pathInfo.info().repoDirName).toBe('main')
      dispose()
    })
  })

  it('skipLoadingFlash suppresses pathInfo.loading while the seed paints the form', async () => {
    // The seed makes showGitOptions truthy; the createGuardedFetch
    // skipLoadingFlash predicate reads that, so the path-info loading
    // flag stays false during the in-flight RPC. Without this, the
    // GitOptionsLoader would flash its spinner over the already-painted
    // form on every dialog open.
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise<never>(() => {}))
    await createRoot(async (dispose) => {
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'feature',
      })
      await flushMicrotasks()
      expect(inspect.pathInfo.loading()).toBe(false)
      dispose()
    })
  })

  it('refines isDirty + isWorktree + branches once the RPC resolves', async () => {
    vi.mocked(workerRpc.inspectBranchChange).mockResolvedValue(makeResp({
      isDirty: true,
      isWorktree: true,
      branches: [
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'main', isRemote: false },
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'feature', isRemote: false },
      ],
    }))
    await createRoot(async (dispose) => {
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      await flushMicrotasks()
      expect(inspect.pathInfo.info().isDirty).toBe(true)
      // isWorktree response means the path resolves to a worktree root,
      // NOT a main repo root. The hook projects this onto isRepoRoot /
      // isWorktreeRoot so GitOptions can render the "Currently in
      // worktree" affordance.
      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(true)
      expect(inspect.pathInfo.info().isRepoRoot).toBe(false)
      expect(inspect.branches()).toHaveLength(2)
      expect(inspect.branchesLoading()).toBe(false)
      dispose()
    })
  })

  it('refresh() re-issues the RPC', async () => {
    vi.mocked(workerRpc.inspectBranchChange).mockResolvedValue(makeResp())
    await createRoot(async (dispose) => {
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      await flushMicrotasks()
      expect(workerRpc.inspectBranchChange).toHaveBeenCalledTimes(1)
      inspect.refresh()
      await flushMicrotasks()
      expect(workerRpc.inspectBranchChange).toHaveBeenCalledTimes(2)
      dispose()
    })
  })

  it('forwards RPC failures to onError', async () => {
    const onError = vi.fn()
    vi.mocked(workerRpc.inspectBranchChange).mockRejectedValue(new Error('boom'))
    await createRoot(async (dispose) => {
      useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
        onError,
      })
      await flushMicrotasks()
      expect(onError).toHaveBeenCalledWith(expect.any(Error))
      dispose()
    })
  })

  it('resets the seed to a non-repo shape on RPC failure (GitOptions hides)', async () => {
    // Regression: the seed paints isGitRepo=true / isRepoRoot=true based
    // on the caller's optimistic belief that the row's gitToplevel is
    // still a git repo. If the worker rejects with "not a git repository"
    // (rm -rf .git, mount loss, permission flip), the dialog was stacking
    // branch radios on top of the error banner because info() still held
    // the seed. The hook now rolls info back so showGitOptions flips
    // false and GitOptionsLoader hides the form.
    vi.mocked(workerRpc.inspectBranchChange).mockRejectedValue(new Error('not a git repository'))
    await createRoot(async (dispose) => {
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'main',
      })
      // Pre-flush: seed is live, form would render.
      expect(inspect.pathInfo.showGitOptions()).toBe(true)
      await flushMicrotasks()
      // Post-flush: seed cleared, showGitOptions false, BranchSelect
      // loading flag also cleared so the dialog isn't stuck on a spinner.
      expect(inspect.pathInfo.info().isGitRepo).toBe(false)
      expect(inspect.pathInfo.info().isRepoRoot).toBe(false)
      expect(inspect.pathInfo.info().isWorktreeRoot).toBe(false)
      expect(inspect.pathInfo.info().repoRoot).toBe('')
      expect(inspect.pathInfo.info().currentBranch).toBe('')
      expect(inspect.pathInfo.showGitOptions()).toBe(false)
      expect(inspect.branchesLoading()).toBe(false)
      dispose()
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
      const inspect = useChangeBranchInspect({
        workerId: 'w1',
        gitToplevel: '',
        branchName: 'main',
        onError,
      })
      await flushMicrotasks()
      expect(workerRpc.inspectBranchChange).not.toHaveBeenCalled()
      expect(onError).toHaveBeenCalledTimes(1)
      expect((onError.mock.calls[0][0] as Error).message).toMatch(/no resolved repo path/)
      expect(inspect.branchesLoading()).toBe(false)
      dispose()
    })
  })
})
