import type { GetGitInfoResponse } from '~/generated/leapmux/v1/git_pb'
import { createEffect, createRoot, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred, flush } from '../../tests/unit/helpers/async'

const getGitInfo = vi.fn<(workerId: string, req: { workerId: string, path: string, orgId: string }) => Promise<GetGitInfoResponse>>()

// Per-test mutable orgId so the orgId-tracking test can drive a change
// from outside the hook. Default `'org-1'` matches the value used
// across the rest of the suite; tests that exercise org switches set
// it explicitly.
const [orgId, setOrgId] = createSignal('org-1')

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId, slug: () => 'admin' }),
}))

vi.mock('~/api/workerRpc', () => ({
  getGitInfo: (workerId: string, req: { workerId: string, path: string, orgId: string }) =>
    getGitInfo(workerId, req),
}))

const { useGitPathInfo } = await import('./useGitPathInfo')

function gitResp(overrides: Partial<GetGitInfoResponse> = {}): GetGitInfoResponse {
  return {
    $typeName: 'leapmux.v1.GetGitInfoResponse',
    isGitRepo: true,
    isWorktree: false,
    repoRoot: '/repo',
    repoDirName: 'repo',
    isRepoRoot: true,
    isWorktreeRoot: false,
    isDirty: false,
    currentBranch: 'main',
    originUrl: '',
    ...overrides,
  } as GetGitInfoResponse
}

beforeEach(() => {
  getGitInfo.mockReset()
  // Reset the shared orgId signal between tests so a prior test's
  // last-set value doesn't leak in as the next test's starting org.
  setOrgId('org-1')
})

describe('useGitPathInfo', () => {
  it('does not probe when workerId or path is empty', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('')
        const [path] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        await flush()
        expect(getGitInfo).not.toHaveBeenCalled()
        expect(info.loading()).toBe(false)
        expect(info.info().isGitRepo).toBe(false)
        expect(info.showGitOptions()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('populates every identity signal from the probe response', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({
      isGitRepo: true,
      isRepoRoot: true,
      isWorktreeRoot: false,
      isDirty: true,
      repoRoot: '/repo',
      repoDirName: 'repo',
      currentBranch: 'feature-x',
    }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        expect(getGitInfo).toHaveBeenCalledTimes(1)
        expect(getGitInfo.mock.calls[0]).toEqual(['A', { workerId: 'A', path: '/repo', orgId: 'org-1' }])
        expect(info.info().isGitRepo).toBe(true)
        expect(info.info().isRepoRoot).toBe(true)
        expect(info.info().isWorktreeRoot).toBe(false)
        expect(info.info().isDirty).toBe(true)
        expect(info.info().repoRoot).toBe('/repo')
        expect(info.info().repoDirName).toBe('repo')
        expect(info.info().currentBranch).toBe('feature-x')
        expect(info.showGitOptions()).toBe(true)
        expect(info.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('showGitOptions tracks isGitRepo && (isRepoRoot || isWorktreeRoot)', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({
      isGitRepo: true,
      isRepoRoot: false,
      isWorktreeRoot: false,
    }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/repo/subdir')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        // Inside a repo but not at the root -- GitOptions hides itself,
        // since it can't safely treat the path as a repo to mutate.
        expect(info.info().isGitRepo).toBe(true)
        expect(info.showGitOptions()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('showGitOptions is true at a worktree root', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({
      isGitRepo: true,
      isRepoRoot: false,
      isWorktreeRoot: true,
    }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/repo/wt')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        expect(info.showGitOptions()).toBe(true)
        dispose()
        done()
      })
    })
  })

  it('loading flips true during the probe and back to false on resolve', async () => {
    const d = deferred<GetGitInfoResponse>()
    getGitInfo.mockImplementationOnce(() => d.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        expect(info.loading()).toBe(true)
        d.resolve(gitResp())
        await flush()
        expect(info.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('re-probes when path changes (e.g. remap to repo root) without flashing showGitOptions off', async () => {
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/repo' }))
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: false, isRepoRoot: true, repoRoot: '/repo' }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path, setPath] = createSignal('/repo/wt')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        expect(info.showGitOptions()).toBe(true)
        expect(info.info().isWorktreeRoot).toBe(true)

        // Caller remaps to the repo root. The hook intentionally
        // skips setLoading(true) when showGitOptions is already
        // true, so consumers don't see the GitOptions panel flicker
        // out during the re-probe.
        setPath('/repo')
        await flush()
        expect(getGitInfo).toHaveBeenCalledTimes(2)
        expect(info.info().isWorktreeRoot).toBe(false)
        expect(info.info().isRepoRoot).toBe(true)
        expect(info.showGitOptions()).toBe(true)
        dispose()
        done()
      })
    })
  })

  it('resets identity to falsy values when workerId becomes empty after a successful probe', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({ isRepoRoot: true }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        expect(info.info().isGitRepo).toBe(true)

        // Worker disconnects / dialog reuses the hook for a new flow.
        setWorkerId('')
        await flush()
        expect(info.info().isGitRepo).toBe(false)
        expect(info.info().repoRoot).toBe('')
        expect(info.info().currentBranch).toBe('')
        expect(info.showGitOptions()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('on RPC failure: logs but does not throw, and clears identity', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({ isGitRepo: true, isRepoRoot: true }))
    getGitInfo.mockRejectedValueOnce(new Error('boom'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path, setPath] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        expect(info.info().isGitRepo).toBe(true)

        // Re-trigger via path change — the production-only refresh
        // pathway (the hook never exposed a manual refresh API).
        setPath('/repo-2')
        await flush()
        expect(info.info().isGitRepo).toBe(false)
        expect(info.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('remapWorktreeRoot option: fires once with repo root when first probe lands on a worktree', async () => {
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/main/repo' }))
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: false, isRepoRoot: true, repoRoot: '/main/repo' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path, setPath] = createSignal('/main/repo-worktrees/feature')
        remap.mockImplementation(setPath)
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        setWorkerId('A')
        await flush()
        expect(remap).toHaveBeenCalledTimes(1)
        expect(remap).toHaveBeenCalledWith('/main/repo')
        // Second probe lands on the canonical repo root — remap must
        // NOT fire again (it's one-shot, gated on the firstProbeSeen flag).
        expect(getGitInfo).toHaveBeenCalledTimes(2)
        expect(remap).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('remapWorktreeRoot option: skips the wasted setInfo so info() never paints the worktree-scoped branch', async () => {
    // Regression guard: an earlier impl wrote the worktree's
    // currentBranch into info() before the remap-triggered second
    // probe overwrote it, so consumers saw the worktree branch flash
    // for one render before the canonical repo root's branch landed.
    // The fix skips the setInfo on the first probe when a remap is
    // about to redirect the path. Verify the dialog never observes
    // the worktree branch in info().
    const wtProbe = deferred<GetGitInfoResponse>()
    const rootProbe = deferred<GetGitInfoResponse>()
    getGitInfo
      .mockReturnValueOnce(wtProbe.promise)
      .mockReturnValueOnce(rootProbe.promise)
    const remap = vi.fn<(p: string) => void>()
    const seen: string[] = []
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path, setPath] = createSignal('/main/repo-worktrees/feature')
        remap.mockImplementation(setPath)
        const info = useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        createEffect(() => {
          seen.push(info.info().currentBranch)
        })
        setWorkerId('A')
        // First probe (worktree) lands.
        wtProbe.resolve(gitResp({
          isWorktreeRoot: true,
          isRepoRoot: false,
          repoRoot: '/main/repo',
          currentBranch: 'worktree-feature',
        }))
        await flush()
        // Second probe (repo root) lands.
        rootProbe.resolve(gitResp({
          isWorktreeRoot: false,
          isRepoRoot: true,
          repoRoot: '/main/repo',
          currentBranch: 'main',
        }))
        await flush()
        // The first observed branch is the initial empty string (from
        // EMPTY_INFO). The next non-empty value must be 'main' (repo
        // root) — never 'worktree-feature'.
        expect(seen[0]).toBe('')
        expect(seen).not.toContain('worktree-feature')
        expect(info.info().currentBranch).toBe('main')
        dispose()
        done()
      })
    })
  })

  it('remapWorktreeRoot option: not called when first probe is a regular repo root', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({ isWorktreeRoot: false, isRepoRoot: true, repoRoot: '/repo' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/repo')
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        setWorkerId('A')
        await flush()
        expect(remap).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('remapWorktreeRoot option: NOT called when isWorktreeRoot=true but repoRoot is empty', async () => {
    // Defensive guard: a malformed probe response shouldn't trigger a
    // remap to "" (which would collapse the dialog's path back to empty
    // and re-probe an empty wid/path no-op).
    getGitInfo.mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path] = createSignal('/some/path')
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        setWorkerId('A')
        await flush()
        expect(remap).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('remapWorktreeRoot option: stays one-shot even when a later probe also returns isWorktreeRoot=true', async () => {
    // The remap is gated on the firstProbeSeen flag. Once the first
    // probe lands (worktree or not), subsequent worktree-root probes
    // must NOT fire it again — otherwise a worker/path switch could
    // spuriously rewrite the user's chosen working dir.
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: false, isRepoRoot: true, repoRoot: '/repo' }))
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/other' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path, setPath] = createSignal('/repo')
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        setWorkerId('A')
        await flush()
        expect(remap).not.toHaveBeenCalled()
        setPath('/other-worktree')
        await flush()
        // firstProbeResult is no longer null, so even though this probe
        // landed on a worktree root, the remap must not fire.
        expect(remap).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('firstProbeSeen: resets when workerId swaps to a different non-empty value so the new worker gets its own remap-on-first-probe', async () => {
    // Regression: firstProbeSeen only reset when workerId/path cycled
    // empty, so a useWorkerDialog-wrapped dialog whose user picks a
    // different non-empty worker silently lost the remap. The new
    // worker may resolve the same path to a worktree root the old one
    // didn't, and the user has NOT manually adjusted the path (they
    // swapped workers, not paths) — so the new worker deserves the
    // same first-probe-remap treatment.
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: false, isRepoRoot: true, repoRoot: '/repo-A' }))
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/repo-B-main' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('A')
        const [path] = createSignal('/some/path')
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        await flush()
        // First probe on worker A — regular repo root, no remap call.
        expect(remap).not.toHaveBeenCalled()
        // Worker swap to B; B's probe lands on a worktree root and the
        // remap MUST fire (pre-fix it would have been silently skipped).
        setWorkerId('B')
        await flush()
        expect(remap).toHaveBeenCalledTimes(1)
        expect(remap).toHaveBeenCalledWith('/repo-B-main')
        dispose()
        done()
      })
    })
  })

  it('firstProbeSeen: keeps the flag set across path changes on the SAME worker (does not steal user input)', async () => {
    // The flag only resets on empty cycle OR worker swap. A path
    // change on the same worker is a user-driven navigation that must
    // NOT trigger a re-remap; doing so would yank the input back to
    // the canonical repo root every time the user pasted a subdir.
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/repo' }))
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/repo' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path, setPath] = createSignal('/wt-1')
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        await flush()
        expect(remap).toHaveBeenCalledTimes(1)
        // Path swap on same worker — no re-remap.
        setPath('/wt-2')
        await flush()
        expect(remap).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('firstProbeSeen: resets after workerId/path goes empty so a re-mount can fire remap again', async () => {
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/main/repo' }))
      .mockResolvedValueOnce(gitResp({ isWorktreeRoot: true, isRepoRoot: false, repoRoot: '/other/repo' }))
    const remap = vi.fn<(p: string) => void>()
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path, setPath] = createSignal('/main/wt')
        useGitPathInfo(workerId, path, { remapWorktreeRoot: remap })
        await flush()
        expect(remap).toHaveBeenCalledTimes(1)
        expect(remap).toHaveBeenCalledWith('/main/repo')
        // Path cycles to empty — flag resets. Next non-empty probe
        // counts as a fresh first probe.
        setPath('')
        await flush()
        setPath('/other/wt')
        await flush()
        expect(remap).toHaveBeenCalledTimes(2)
        expect(remap).toHaveBeenLastCalledWith('/other/repo')
        dispose()
        done()
      })
    })
  })

  it('identical-payload re-probe does not refire downstream accessors (change-detection guard)', async () => {
    // The applySuccess guard skips setInfo when every field equals the
    // previous record. Without it, a re-probe (e.g. path-toggle) that
    // lands on the same payload would replace the signal value by
    // identity and refire every dependent createEffect — wasted
    // renders for parent dialogs whose layouts depend on the accessor.
    const sameResp = gitResp({ isWorktreeRoot: false, isRepoRoot: true, repoRoot: '/repo', currentBranch: 'main' })
    getGitInfo
      .mockResolvedValueOnce(sameResp)
      .mockResolvedValueOnce(sameResp)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path, setPath] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        await flush()

        let observed = 0
        createEffect(() => {
          void info.info().currentBranch
          observed++
        })
        // First effect-run is the initial subscription; baseline.
        expect(observed).toBe(1)

        // Re-probe by changing the path the production reactive
        // pathway uses to retrigger.
        setPath('/repo-2')
        await flush()
        // Second probe returned an identical payload — the guard skips
        // setInfo, so the effect must NOT re-run.
        expect(observed).toBe(1)
        dispose()
        done()
      })
    })
  })

  it('changed-payload re-probe DOES refire downstream accessors (guard is not over-eager)', async () => {
    // Companion to the no-op test: when any field differs, setInfo
    // still runs and downstream effects see the new value. Catches
    // a regression where the equality check is too loose.
    getGitInfo
      .mockResolvedValueOnce(gitResp({ isRepoRoot: true, repoRoot: '/repo', currentBranch: 'main' }))
      .mockResolvedValueOnce(gitResp({ isRepoRoot: true, repoRoot: '/repo', currentBranch: 'feature' }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path, setPath] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        await flush()

        const seen: string[] = []
        createEffect(() => {
          seen.push(info.info().currentBranch)
        })
        expect(seen).toEqual(['main'])

        setPath('/repo-2')
        await flush()
        expect(seen).toEqual(['main', 'feature'])
        dispose()
        done()
      })
    })
  })

  it('error-path setInfo is guarded against no-op writes when info() is already EMPTY_INFO', async () => {
    // Regression guard for the onError change-detection: when a probe
    // rejects while info() already equals EMPTY_INFO, setInfo(EMPTY_INFO)
    // must NOT fire (the replace-by-identity would re-notify every
    // downstream consumer for a value that didn't change). The first
    // failure clears info() to EMPTY_INFO and lets the effect baseline;
    // the second consecutive failure must leave the subscriber's count
    // untouched.
    getGitInfo
      .mockRejectedValueOnce(new Error('probe-1 failed'))
      .mockRejectedValueOnce(new Error('probe-2 failed'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path, setPath] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        await flush()
        // After the first failure, info() is EMPTY_INFO.
        expect(info.info().isGitRepo).toBe(false)

        let observed = 0
        createEffect(() => {
          void info.info().currentBranch
          observed++
        })
        expect(observed).toBe(1)

        // Trigger another probe — also rejects, also lands on
        // EMPTY_INFO. With the guard, downstream effects do NOT re-run.
        setPath('/repo-2')
        await flush()
        expect(observed).toBe(1)
        dispose()
        done()
      })
    })
  })

  it('does not refire the probe when orgId notifies with an unchanged value', async () => {
    // OrgContext can re-emit the same orgId (e.g. after a `listMyOrgs`
    // refetch returns the same list). The tracked tuple goes through a
    // memo with `shallowEqualArrays`, so identity churn upstream that
    // doesn't change the value tuple must NOT fire a redundant RPC.
    getGitInfo.mockResolvedValue(gitResp({ currentBranch: 'main' }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path] = createSignal('/repo')
        useGitPathInfo(workerId, path)
        await flush()
        expect(getGitInfo).toHaveBeenCalledTimes(1)

        // Re-set orgId to the same value — Solid's default signal
        // equality skips the write, but if a future refactor used
        // `equals: false` (or replaced this with a memo that fires on
        // identity), the dedup would still catch it.
        setOrgId('org-1')
        await flush()
        expect(getGitInfo).toHaveBeenCalledTimes(1)
        dispose()
        done()
      })
    })
  })

  it('refires the probe when orgId changes even if workerId and path are unchanged', async () => {
    // The fetch closure reads `org.orgId()` at call time, so an org
    // switch without a workerId/path change would otherwise bake the
    // stale orgId into the next RPC. Tracking orgId in the on() tuple
    // forces a refresh tied to the org switch.
    getGitInfo
      .mockResolvedValueOnce(gitResp({ currentBranch: 'main' }))
      .mockResolvedValueOnce(gitResp({ currentBranch: 'main' }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId] = createSignal('A')
        const [path] = createSignal('/repo')
        useGitPathInfo(workerId, path)
        await flush()
        expect(getGitInfo).toHaveBeenCalledTimes(1)
        expect(getGitInfo.mock.calls[0][1].orgId).toBe('org-1')

        setOrgId('org-2')
        await flush()
        expect(getGitInfo).toHaveBeenCalledTimes(2)
        expect(getGitInfo.mock.calls[1][1].orgId).toBe('org-2')
        dispose()
        done()
      })
    })
  })

  it('discards stale responses when a newer probe is started before the previous resolves', async () => {
    const slow = deferred<GetGitInfoResponse>()
    const fast = deferred<GetGitInfoResponse>()
    getGitInfo.mockImplementationOnce(() => slow.promise)
    getGitInfo.mockImplementationOnce(() => fast.promise)
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const [workerId, setWorkerId] = createSignal('')
        const [path, setPath] = createSignal('/repo')
        const info = useGitPathInfo(workerId, path)
        setWorkerId('A')
        await flush()
        // First probe in flight for /repo.
        setPath('/other')
        await flush()
        // Second probe in flight for /other. Resolve the SECOND first
        // (fast path returns first), then the stale slow one.
        fast.resolve(gitResp({ currentBranch: 'other-branch' }))
        await flush()
        expect(info.info().currentBranch).toBe('other-branch')
        slow.resolve(gitResp({ currentBranch: 'stale-branch' }))
        await flush()
        // The stale response must NOT overwrite the fresh one.
        expect(info.info().currentBranch).toBe('other-branch')
        dispose()
        done()
      })
    })
  })

  describe('seed option', () => {
    it('pre-fills the snapshot so showGitOptions() returns true before the probe lands', async () => {
      const pending = deferred<GetGitInfoResponse>()
      getGitInfo.mockReturnValueOnce(pending.promise)
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const [workerId] = createSignal('A')
          const [path] = createSignal('/repo')
          const info = useGitPathInfo(workerId, path, {
            seed: {
              isGitRepo: true,
              isRepoRoot: true,
              repoRoot: '/repo',
              repoDirName: 'repo',
            },
          })
          // No flushing yet: every read must work off the seed.
          expect(info.info().isGitRepo).toBe(true)
          expect(info.info().isRepoRoot).toBe(true)
          expect(info.info().repoRoot).toBe('/repo')
          expect(info.info().repoDirName).toBe('repo')
          expect(info.showGitOptions()).toBe(true)
          // The probe is in flight but loading must stay false because
          // showGitOptions is already true (skipLoadingFlash kicks in).
          await flush()
          expect(info.loading()).toBe(false)
          pending.resolve(gitResp({ currentBranch: 'main', isDirty: true }))
          await flush()
          // The probe lands and fills in the volatile fields.
          expect(info.info().currentBranch).toBe('main')
          expect(info.info().isDirty).toBe(true)
          dispose()
          done()
        })
      })
    })

    it('lets the probe correct seeded fields that turned out to be wrong', async () => {
      // Caller guesses isRepoRoot=true but the probe reveals a worktree.
      getGitInfo.mockResolvedValueOnce(gitResp({
        isGitRepo: true,
        isRepoRoot: false,
        isWorktreeRoot: true,
        repoRoot: '/parent',
        repoDirName: 'parent',
        currentBranch: 'feature',
      }))
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const [workerId] = createSignal('A')
          const [path] = createSignal('/wt')
          const info = useGitPathInfo(workerId, path, {
            seed: {
              isGitRepo: true,
              isRepoRoot: true,
              repoRoot: '/wt',
              repoDirName: 'wt',
            },
          })
          await flush()
          // Probe corrects the seed.
          expect(info.info().isRepoRoot).toBe(false)
          expect(info.info().isWorktreeRoot).toBe(true)
          expect(info.info().repoRoot).toBe('/parent')
          expect(info.info().repoDirName).toBe('parent')
          dispose()
          done()
        })
      })
    })

    it('showGitOptions stays stable across probe-driven info() updates that do not flip the boolean', async () => {
      // Regression guard: showGitOptions is a createMemo so downstream
      // `on()` consumers (e.g. GitOptions' listGitBranches effect)
      // don't refire on every info() update. Without the memo, a seed
      // + probe-lands with same showGitOptions=true would refire
      // tracking consumers and cause duplicate downstream RPCs.
      getGitInfo.mockResolvedValueOnce(gitResp({
        isGitRepo: true,
        isRepoRoot: true,
        currentBranch: 'feature', // volatile field changes vs. seed
        isDirty: true,
      }))
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const [workerId] = createSignal('A')
          const [path] = createSignal('/repo')
          const info = useGitPathInfo(workerId, path, {
            seed: {
              isGitRepo: true,
              isRepoRoot: true,
              repoRoot: '/repo',
              repoDirName: 'repo',
            },
          })
          const observed: boolean[] = []
          createEffect(() => {
            observed.push(info.showGitOptions())
          })
          // Initial subscription captures the seed value.
          await flush()
          // Probe lands; volatile fields change but showGitOptions
          // stays true, so the effect must NOT refire.
          await flush()
          expect(observed).toEqual([true])
          dispose()
          done()
        })
      })
    })

    it('showGitOptions flips and refires consumers when the probe corrects isRepoRoot=true to a non-repo', async () => {
      // Symmetry check: when showGitOptions actually changes, the memo
      // does fire downstream effects.
      getGitInfo.mockResolvedValueOnce(gitResp({
        isGitRepo: false,
        isRepoRoot: false,
        isWorktreeRoot: false,
      }))
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const [workerId] = createSignal('A')
          const [path] = createSignal('/not-a-repo')
          const info = useGitPathInfo(workerId, path, {
            seed: {
              isGitRepo: true,
              isRepoRoot: true,
              repoRoot: '/not-a-repo',
              repoDirName: 'not-a-repo',
            },
          })
          const observed: boolean[] = []
          createEffect(() => {
            observed.push(info.showGitOptions())
          })
          await flush()
          await flush()
          expect(observed).toEqual([true, false])
          dispose()
          done()
        })
      })
    })

    it('skips the empty-branch setInfo when a seed is structurally equal to EMPTY_INFO', async () => {
      // Regression guard: the empty-wid/path branch used to compare info()
      // to EMPTY_INFO by reference, so a seed object that happened to
      // match EMPTY_INFO field-for-field still tripped a setInfo(EMPTY_INFO)
      // and re-emitted every downstream accessor for no value. The
      // shallow-equal check suppresses the redundant write.
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const [workerId] = createSignal('')
          const [path] = createSignal('/repo')
          const info = useGitPathInfo(workerId, path, {
            seed: { isGitRepo: false },
          })
          const observed: boolean[] = []
          createEffect(() => {
            observed.push(info.info().isGitRepo)
          })
          await flush()
          // Mount-time subscription captures the seed value once.
          // The empty-wid branch must not refire it.
          expect(observed).toEqual([false])
          expect(getGitInfo).not.toHaveBeenCalled()
          dispose()
          done()
        })
      })
    })
  })
})
