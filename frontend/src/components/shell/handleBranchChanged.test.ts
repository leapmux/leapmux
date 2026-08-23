/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { handleBranchChanged } from './handleBranchChanged'

const mockGetGitFileStatus = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getGitFileStatus: (...a: unknown[]) => mockGetGitFileStatus(...a),
}))

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

beforeEach(() => {
  mockGetGitFileStatus.mockReset()
  mockGetGitFileStatus.mockResolvedValue({
    repoRoot: '/repo',
    status: { toplevel: '/repo', branch: 'feature', originUrl: 'o' },
    files: [],
  })
})

describe('handleBranchChanged', () => {
  it('stamps the new branch on the repo-keyed store', async () => {
    const repoGitStore = createRepoGitStore()
    repoGitStore.upsert(repoKey('w1', '/repo'), {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
    })

    handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/repo' } as never) },
      { workerId: 'w1', gitToplevel: '/repo' },
      'feature',
    )

    expect(repoGitStore.get(repoKey('w1', '/repo'))?.branch).toBe('feature')
    expect(repoGitStore.get(repoKey('w1', '/repo'))?.branchPinnedUntilRefresh).toBe(true)
    await flush()
  })

  it('refreshes git status for the active repo', async () => {
    const repoGitStore = createRepoGitStore()
    const refresh = vi.spyOn(repoGitStore, 'refresh').mockResolvedValue(undefined)

    handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/repo' } as never) },
      { workerId: 'w1', gitToplevel: '/repo' },
      'feature',
    )
    await flush()

    expect(refresh).toHaveBeenCalledWith('w1', '/repo', { repoKey: repoKey('w1', '/repo') })
    expect(mockGetGitFileStatus).not.toHaveBeenCalled()
  })

  it('fetches git status directly for a non-active repo', async () => {
    const repoGitStore = createRepoGitStore()
    const refresh = vi.spyOn(repoGitStore, 'refresh').mockResolvedValue(undefined)
    repoGitStore.upsert(repoKey('w1', '/other'), {
      workerId: 'w1',
      toplevel: '/other',
      branch: 'dev',
      diffAdded: 3,
    })
    mockGetGitFileStatus.mockResolvedValueOnce({
      repoRoot: '/other',
      status: { toplevel: '/other', branch: 'feature', originUrl: 'o' },
      files: [],
    })

    handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/active' } as never) },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(mockGetGitFileStatus).toHaveBeenCalled()
    })

    expect(refresh).not.toHaveBeenCalled()
    expect(mockGetGitFileStatus).toHaveBeenCalledWith('w1', { workerId: 'w1', path: '/other' })
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.diffAdded).toBe(0)
  })

  it('does not stamp when the repo path never resolved', async () => {
    const repoGitStore = createRepoGitStore()

    handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({} as never) },
      { workerId: 'w1', gitToplevel: '' },
      'feature',
    )
    await flush()

    expect(repoGitStore.repos()).toEqual({})
  })

  it('does not throw when the active-repo refresh is in flight', async () => {
    const repoGitStore = createRepoGitStore()
    vi.spyOn(repoGitStore, 'refresh').mockResolvedValue(undefined)

    expect(() => handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/repo' } as never) },
      { workerId: 'w1', gitToplevel: '/repo' },
      'feature',
    )).not.toThrow()
    await flush()
  })

  it('writes a non-repo stub when refreshing a non-active repo fails as non-git', async () => {
    const repoGitStore = createRepoGitStore()
    mockGetGitFileStatus.mockResolvedValueOnce({
      repoRoot: '',
      status: undefined,
      files: [],
      errorHint: 'not a git repository',
    })

    handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/active' } as never) },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(repoGitStore.get(repoKey('w1', '/other'))?.errorHint).toBe('not a git repository')
    })
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
  })

  it('does not change focused key when refreshing a non-active repo', async () => {
    const repoGitStore = createRepoGitStore()
    repoGitStore.setFocusedKey(repoKey('w1', '/active'))
    repoGitStore.upsert(repoKey('w1', '/active'), { workerId: 'w1', toplevel: '/active', branch: 'main' })
    repoGitStore.upsert(repoKey('w1', '/other'), { workerId: 'w1', toplevel: '/other', branch: 'dev' })

    handleBranchChanged(
      { repoGitStore, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/active' } as never) },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()

    expect(repoGitStore.focusedKey()).toBe(repoKey('w1', '/active'))
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
  })
})
