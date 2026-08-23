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
      gitStatusSeen: true,
    })

    handleBranchChanged(
      { repoGitStore },
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
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/repo' },
      'feature',
    )
    await flush()

    expect(refresh).toHaveBeenCalledWith('w1', '/repo', { repoKey: repoKey('w1', '/repo') })
  })

  it('refreshes git status for a non-active repo through the store', async () => {
    const repoGitStore = createRepoGitStore()
    const refresh = vi.spyOn(repoGitStore, 'refresh').mockResolvedValue(undefined)

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()

    expect(refresh).toHaveBeenCalledWith('w1', '/other', { repoKey: repoKey('w1', '/other') })
  })

  it('does not stamp when the repo path never resolved', async () => {
    const repoGitStore = createRepoGitStore()

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '' },
      'feature',
    )
    await flush()

    expect(repoGitStore.repos()).toEqual({})
  })

  it('does not throw when refresh is in flight', async () => {
    const repoGitStore = createRepoGitStore()
    vi.spyOn(repoGitStore, 'refresh').mockResolvedValue(undefined)

    expect(() => handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/repo' },
      'feature',
    )).not.toThrow()
    await flush()
  })

  it('writes a non-repo stub without inventing a toplevel', async () => {
    const repoGitStore = createRepoGitStore()
    mockGetGitFileStatus.mockResolvedValueOnce({
      repoRoot: '',
      status: undefined,
      files: [],
      errorHint: 'not a git repository',
    })

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(repoGitStore.get(repoKey('w1', '/other'))?.errorHint).toBe('not a git repository')
    })
    expect(repoGitStore.get(repoKey('w1', '/other'))?.toplevel).toBe('')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('')
  })

  it('keeps file state when refresh returns a transient non-repo response', async () => {
    const repoGitStore = createRepoGitStore()
    const files = [{ path: 'a.txt' } as never]
    repoGitStore.upsert(repoKey('w1', '/other'), {
      workerId: 'w1',
      toplevel: '/other',
      branch: 'dev',
      diffAdded: 3,
      files,
      gitStatusSeen: true,
    })
    mockGetGitFileStatus.mockResolvedValueOnce({
      repoRoot: '',
      status: undefined,
      files: [],
      errorHint: 'not a git repository',
    })

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(repoGitStore.get(repoKey('w1', '/other'))?.branchPinnedUntilRefresh).toBe(false)
    })

    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.toplevel).toBe('/other')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.diffAdded).toBe(3)
    expect(repoGitStore.get(repoKey('w1', '/other'))?.files).toEqual(files)
    expect(repoGitStore.get(repoKey('w1', '/other'))?.errorHint).toBe('')
  })

  it('keeps a metadata-only clean repo across a transient non-repo response', async () => {
    const repoGitStore = createRepoGitStore()
    repoGitStore.upsert(repoKey('w1', '/other'), {
      workerId: 'w1',
      toplevel: '/other',
      branch: 'dev',
      gitStatusSeen: true,
    })
    mockGetGitFileStatus.mockResolvedValueOnce({
      repoRoot: '',
      status: undefined,
      files: [],
      errorHint: 'not a git repository',
    })

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(repoGitStore.get(repoKey('w1', '/other'))?.branchPinnedUntilRefresh).toBe(false)
    })

    expect(repoGitStore.get(repoKey('w1', '/other'))?.toplevel).toBe('/other')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.errorHint).toBe('')
    expect(repoGitStore.get(repoKey('w1', '/other'))?.gitStatusSeen).toBe(true)
  })

  it('clears the branch pin when refresh rejects', async () => {
    const repoGitStore = createRepoGitStore()
    mockGetGitFileStatus.mockRejectedValueOnce(new Error('worker unreachable'))

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(repoGitStore.get(repoKey('w1', '/other'))?.branchPinnedUntilRefresh).toBe(false)
    })
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
  })

  it('does not change focused key when refreshing a non-active repo', async () => {
    const repoGitStore = createRepoGitStore()
    repoGitStore.setFocusedKey(repoKey('w1', '/active'))
    repoGitStore.upsert(repoKey('w1', '/active'), { workerId: 'w1', toplevel: '/active', branch: 'main', gitStatusSeen: true })
    repoGitStore.upsert(repoKey('w1', '/other'), { workerId: 'w1', toplevel: '/other', branch: 'dev', gitStatusSeen: true })
    mockGetGitFileStatus.mockResolvedValueOnce({
      repoRoot: '/other',
      status: { toplevel: '/other', branch: 'feature', originUrl: 'o' },
      files: [],
    })

    handleBranchChanged(
      { repoGitStore },
      { workerId: 'w1', gitToplevel: '/other' },
      'feature',
    )
    await flush()
    await vi.waitFor(() => {
      expect(repoGitStore.get(repoKey('w1', '/other'))?.branchPinnedUntilRefresh).toBe(false)
    })

    expect(repoGitStore.focusedKey()).toBe(repoKey('w1', '/active'))
    expect(repoGitStore.get(repoKey('w1', '/other'))?.branch).toBe('feature')
  })
})
