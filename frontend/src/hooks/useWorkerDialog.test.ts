import type { GetGitInfoResponse } from '~/generated/leapmux/v1/git_pb'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workerClient } from '~/api/clients'
import { GitMode } from '~/hooks/useGitModeState'
import { flush } from '~/test-support/async'

const getGitInfo = vi.fn<(workerId: string, req: { workerId: string, path: string, orgId: string }) => Promise<GetGitInfoResponse>>()

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: vi.fn().mockResolvedValue({ workers: [] }),
  },
}))

vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    fetchWorkerInfo: vi.fn(),
    workerInfo: () => null,
    getHomeDir: () => '/home/u',
    getOs: () => 'linux',
  },
}))

vi.mock('~/api/workerRpc', () => ({
  getGitInfo: (workerId: string, req: { workerId: string, path: string, orgId: string }) =>
    getGitInfo(workerId, req),
}))

const { useWorkerDialog } = await import('~/hooks/useWorkerDialog')

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
  vi.clearAllMocks()
  getGitInfo.mockReset()
})

describe('useWorkerDialog', () => {
  it('threads singleWorkerId through to worker and skips listWorkers', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { worker } = useWorkerDialog({
          worker: { singleWorkerId: 'w-fixed', defaultWorkingDir: '/repo' },
        })
        expect(worker.workerId()).toBe('w-fixed')
        expect(worker.workingDir()).toBe('/repo')
        await flush()
        expect(workerClient.listWorkers).not.toHaveBeenCalled()
        dispose()
        done()
      })
    })
  })

  it('shares one error sink across submit failures and listWorkers failures', async () => {
    vi.mocked(workerClient.listWorkers).mockRejectedValueOnce(new Error('fleet listing down'))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { submit } = useWorkerDialog({
          submit: { formatError: () => 'submit failed' },
        })
        // Fleet listing fails on mount; the worker context's onError is
        // wired to submit.setError, so the error surfaces in the submit
        // sink.
        await flush()
        expect(submit.error()).toBe('fleet listing down')
        // A separate submit failure overwrites the error via formatError.
        await submit.run(async () => {
          throw new Error('boom')
        })
        expect(submit.error()).toBe('submit failed')
        dispose()
        done()
      })
    })
  })

  it('forwards pathInfo.seed so showGitOptions is true before the probe lands', async () => {
    // Probe never resolves — we're proving the seed is enough to render.
    getGitInfo.mockReturnValueOnce(new Promise(() => {}))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { pathInfo } = useWorkerDialog({
          worker: { singleWorkerId: 'w1', defaultWorkingDir: '/repo' },
          pathInfo: {
            seed: {
              isGitRepo: true,
              isRepoRoot: true,
              repoRoot: '/repo',
              repoDirName: 'repo',
            },
          },
        })
        // Seed values readable before any microtask drains.
        expect(pathInfo.info().isGitRepo).toBe(true)
        expect(pathInfo.info().isRepoRoot).toBe(true)
        expect(pathInfo.showGitOptions()).toBe(true)
        await flush()
        // Probe still in flight, but the seed kept loading at false.
        expect(pathInfo.loading()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('does not seed by default — pathInfo starts empty without pathInfo.seed', async () => {
    getGitInfo.mockReturnValueOnce(new Promise(() => {}))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { pathInfo } = useWorkerDialog({
          worker: { singleWorkerId: 'w1', defaultWorkingDir: '/repo' },
        })
        expect(pathInfo.info().isGitRepo).toBe(false)
        expect(pathInfo.showGitOptions()).toBe(false)
        dispose()
        done()
      })
    })
  })

  it('pathInfo.remapWorktreeRoot=true reroutes the working dir on the first worktree-root probe', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({
      isGitRepo: true,
      isRepoRoot: false,
      isWorktreeRoot: true,
      repoRoot: '/parent',
      repoDirName: 'parent',
    }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { worker } = useWorkerDialog({
          worker: { singleWorkerId: 'w1', defaultWorkingDir: '/wt' },
          pathInfo: { remapWorktreeRoot: true },
        })
        await flush()
        expect(worker.workingDir()).toBe('/parent')
        dispose()
        done()
      })
    })
  })

  it('exposes worker, gitMode, pathInfo and submit as separate siblings (no flat spread)', async () => {
    // Regression guard for the post-simplify shape. Each sub-hook owns
    // its own surface — flattening them back into a single `state` would
    // re-introduce the "which hook owns this field?" ambiguity the
    // refactor removed.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const dialog = useWorkerDialog({
          worker: { singleWorkerId: 'w-shape', defaultWorkingDir: '/repo' },
        })
        // Sibling namespaces — every field lives under exactly one.
        expect(typeof dialog.worker.workerId).toBe('function')
        expect(typeof dialog.worker.workingDir).toBe('function')
        expect(typeof dialog.gitMode.gitMode).toBe('function')
        expect(typeof dialog.gitMode.toGitFields).toBe('function')
        expect(typeof dialog.gitMode.currentIntent).toBe('function')
        expect(typeof dialog.pathInfo.info).toBe('function')
        expect(typeof dialog.submit.run).toBe('function')
        // Git-mode fields are NOT spread onto `worker` anymore.
        expect((dialog.worker as unknown as Record<string, unknown>).gitMode).toBeUndefined()
        expect((dialog.worker as unknown as Record<string, unknown>).toGitFields).toBeUndefined()
        expect((dialog.worker as unknown as Record<string, unknown>).currentIntent).toBeUndefined()
        await flush()
        dispose()
        done()
      })
    })
  })

  it('forwards gitMode.initialIntent to useGitModeState so the initial intent paints on first render', async () => {
    // ChangeBranchDialog opens on SwitchBranch (not the Current default).
    // The initialIntent must thread through useWorkerDialog → useGitModeState
    // so `gitMode.currentIntent()` returns the seeded variant
    // synchronously — without it, GitOptions would briefly show Current
    // before the first emit replaces it.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { gitMode } = useWorkerDialog({
          worker: { singleWorkerId: 'w1' },
          gitMode: {
            initialIntent: {
              mode: GitMode.SwitchBranch,
              checkoutBranch: 'release',
              checkoutBranchError: null,
            },
          },
        })
        expect(gitMode.gitMode()).toBe(GitMode.SwitchBranch)
        expect(gitMode.currentIntent()).toEqual({
          mode: GitMode.SwitchBranch,
          checkoutBranch: 'release',
          checkoutBranchError: null,
        })
        // toGitFields routes through the seeded intent.
        expect(gitMode.toGitFields()).toMatchObject({
          checkoutBranch: 'release',
          createBranch: '',
          createWorktree: false,
        })
        await flush()
        dispose()
        done()
      })
    })
  })

  it('omitting gitMode option defaults to GitMode.Current (unseeded behaviour preserved)', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { gitMode } = useWorkerDialog({
          worker: { singleWorkerId: 'w1' },
        })
        expect(gitMode.gitMode()).toBe(GitMode.Current)
        expect(gitMode.currentIntent()).toEqual({ mode: GitMode.Current })
        await flush()
        dispose()
        done()
      })
    })
  })

  it('pathInfo.remapWorktreeRoot=false leaves the working dir pinned to the caller-provided path', async () => {
    getGitInfo.mockResolvedValueOnce(gitResp({
      isGitRepo: true,
      isRepoRoot: false,
      isWorktreeRoot: true,
      repoRoot: '/parent',
      repoDirName: 'parent',
    }))
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const { worker } = useWorkerDialog({
          worker: { singleWorkerId: 'w1', defaultWorkingDir: '/wt' },
          // Default: no remap.
        })
        await flush()
        expect(worker.workingDir()).toBe('/wt')
        dispose()
        done()
      })
    })
  })
})
