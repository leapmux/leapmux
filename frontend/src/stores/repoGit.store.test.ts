import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'

const mockGetGitFileStatus = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getGitFileStatus: (...args: unknown[]) => mockGetGitFileStatus(...args),
}))

function makeEntry(overrides: Partial<GitFileStatusEntry> & { path: string }): GitFileStatusEntry {
  return {
    $typeName: 'leapmux.v1.GitFileStatusEntry',
    stagedStatus: GitFileStatusCode.UNSPECIFIED,
    unstagedStatus: GitFileStatusCode.UNSPECIFIED,
    linesAdded: 0,
    linesDeleted: 0,
    stagedLinesAdded: 0,
    stagedLinesDeleted: 0,
    oldPath: '',
    isDir: false,
    ...overrides,
  }
}

describe('createRepoGitStore', () => {
  describe('worker key index', () => {
    it('tracks keys per worker and drops them on clear', () => {
      const store = createRepoGitStore()
      const key = repoKey('w1', '/repo')
      store.upsert(key, { workerId: 'w1', toplevel: '/repo', branch: 'main' })
      expect(store.keysForWorker('w1')).toEqual([key])

      store.clear(key)
      expect(store.keysForWorker('w1')).toEqual([])
    })

    it('moves a key when workerId changes', () => {
      const store = createRepoGitStore()
      const key = repoKey('w1', '/repo')
      store.upsert(key, { workerId: 'w1', toplevel: '/repo', branch: 'main' })
      store.upsert(key, { workerId: 'w2' })

      expect(store.keysForWorker('w1')).toEqual([])
      expect(store.keysForWorker('w2')).toEqual([key])
    })

    it('clears the index on clearAll', () => {
      const store = createRepoGitStore()
      store.upsert(repoKey('w1', '/a'), { workerId: 'w1', toplevel: '/a' })
      store.upsert(repoKey('w2', '/b'), { workerId: 'w2', toplevel: '/b' })
      store.clearAll()
      expect(store.keysForWorker('w1')).toEqual([])
      expect(store.keysForWorker('w2')).toEqual([])
    })
  })

  describe('upsert', () => {
    it('merges partial patches onto existing repo state', () => {
      const store = createRepoGitStore()
      const key = repoKey('w1', '/repo')

      store.upsert(key, {
        workerId: 'w1',
        toplevel: '/repo',
        branch: 'main',
        originUrl: 'git@example.com:o/r.git',
        diffAdded: 4,
        diffDeleted: 1,
      })
      store.upsert(key, { branch: 'feature', diffAdded: 9 })

      const state = store.get(key)!
      expect(state.branch).toBe('feature')
      expect(state.originUrl).toBe('git@example.com:o/r.git')
      expect(state.diffAdded).toBe(9)
      expect(state.diffDeleted).toBe(1)
    })

    it('clears branch when an empty string is written', () => {
      const store = createRepoGitStore()
      const key = repoKey('w1', '/repo')

      store.upsert(key, { workerId: 'w1', toplevel: '/repo', branch: 'main' })
      store.upsert(key, { branch: '' })

      expect(store.get(key)?.branch).toBe('')
    })
  })

  describe('file reconcile', () => {
    it('updates entries in place and preserves survivor object identity', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')

        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED }),
            makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.MODIFIED }),
          ],
        })
        const survivor = store.get(key)!.files[0]

        store.upsert(key, {
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 2 }),
            makeEntry({ path: 'c.txt', unstagedStatus: GitFileStatusCode.UNTRACKED }),
          ],
        })

        const files = store.get(key)!.files
        expect(files.map(f => f.path)).toEqual(['a.txt', 'c.txt'])
        expect(files[0]).toBe(survivor)
        expect(files[0].linesAdded).toBe(2)

        dispose()
      })
    })
  })

  describe('refresh generation guard', () => {
    it('ignores a stale refresh result when a newer refresh started', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        let resolveFirst!: (value: unknown) => void
        const first = new Promise((resolve) => {
          resolveFirst = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(first)
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            status: { toplevel: '/repo', branch: 'new' },
            files: [],
          })

        const slow = store.refresh('worker1', '/repo')
        const fast = store.refresh('worker1', '/repo')
        resolveFirst({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'stale' },
          files: [],
        })

        await Promise.all([slow, fast])

        expect(store.get(repoKey('worker1', '/repo'))?.branch).toBe('new')
        expect(mockGetGitFileStatus).toHaveBeenCalledTimes(2)

        dispose()
      })
    })
  })

  describe('refresh failure', () => {
    it('does not corrupt the focused repo when a different repo refresh fails', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const activeKey = repoKey('worker1', '/active')
        const otherKey = repoKey('worker1', '/other')

        store.setFocusedKey(activeKey)
        store.upsert(activeKey, {
          workerId: 'worker1',
          toplevel: '/active',
          branch: 'main',
          diffAdded: 5,
        })
        store.upsert(otherKey, {
          workerId: 'worker1',
          toplevel: '/other',
          branch: 'dev',
          diffAdded: 2,
        })

        mockGetGitFileStatus.mockRejectedValueOnce(new Error('worker unreachable'))

        await store.refresh('worker1', '/other', { repoKey: otherKey })

        expect(store.focusedKey()).toBe(activeKey)
        expect(store.get(activeKey)?.branch).toBe('main')
        expect(store.get(activeKey)?.diffAdded).toBe(5)
        expect(store.get(otherKey)?.branch).toBe('dev')
        expect(store.get(otherKey)?.diffAdded).toBe(2)

        dispose()
      })
    })

    it('does not reject when the RPC fails', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        mockGetGitFileStatus.mockRejectedValueOnce(new Error('worker unreachable'))
        await expect(store.refresh('worker1', '/repo')).resolves.toBeUndefined()
        dispose()
      })
    })

    it('keeps last-good repo state when a non-repo response targets a healthy hinted key', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, { workerId: 'worker1', toplevel: '/repo', branch: 'main', gitStatusSeen: true })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        })

        await store.refresh('worker1', '/plain-dir', { repoKey: key })

        const state = store.get(key)!
        expect(state.branch).toBe('main')
        expect(state.toplevel).toBe('/repo')
        expect(state.errorHint).toBe('')
        dispose()
      })
    })

    it('persists errorHint on a non-repo probe using the path when repoKey is omitted', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/plain-dir')

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        })

        await store.refresh('worker1', '/plain-dir')

        expect(store.get(key)?.errorHint).toBe('not a git repository')
        dispose()
      })
    })

    it('realigns focusedKey to the canonical toplevel after refresh', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const probeKey = repoKey('worker1', '/repo/pkg')
        store.setFocusedKey(probeKey)

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'main' },
          files: [{ path: 'a.txt' }],
        })

        const written = await store.refresh('worker1', '/repo/pkg', { repoKey: probeKey })

        expect(written).toBe(repoKey('worker1', '/repo'))
        expect(store.focusedKey()).toBe(repoKey('worker1', '/repo'))
        expect(store.get(probeKey)).toBeUndefined()
        dispose()
      })
    })

    it('does not overwrite a healthy repo with a transient non-repo response', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'main',
          diffAdded: 5,
        })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        })

        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branch).toBe('main')
        expect(store.get(key)?.toplevel).toBe('/repo')
        expect(store.get(key)?.diffAdded).toBe(5)
        expect(store.get(key)?.errorHint).toBe('')
        dispose()
      })
    })

    it('clears branchPinnedUntilRefresh when refresh fails', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        mockGetGitFileStatus.mockRejectedValueOnce(new Error('worker unreachable'))

        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branch).toBe('feature')
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)
        dispose()
      })
    })
  })
})
