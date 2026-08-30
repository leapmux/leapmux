import type { GitFileStatusEntry } from '~/generated/proto/leapmux/v1/common_pb'
import { createMemo, createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GitFileStatusCode } from '~/generated/proto/leapmux/v1/common_pb'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'

const mockGetGitFileStatus = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getGitFileStatus: (...args: unknown[]) => mockGetGitFileStatus(...args),
}))

beforeEach(() => {
  mockGetGitFileStatus.mockReset()
})

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

    it('recomputation tracks a workerId move on both sides of the index', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('w1', '/repo')
        let readsW1 = 0
        let readsW2 = 0
        const countW1 = createMemo(() => {
          readsW1++
          return store.keysForWorker('w1').length
        })
        const countW2 = createMemo(() => {
          readsW2++
          return store.keysForWorker('w2').length
        })

        store.upsert(key, { workerId: 'w1', toplevel: '/repo' })
        expect(countW1()).toBe(1)
        expect(countW2()).toBe(0)
        const afterAddW1 = readsW1
        const afterAddW2 = readsW2

        store.upsert(key, { workerId: 'w2' })
        expect(countW1()).toBe(0)
        expect(countW2()).toBe(1)
        expect(readsW1).toBeGreaterThan(afterAddW1)
        expect(readsW2).toBeGreaterThan(afterAddW2)

        dispose()
      })
    })

    it('clears the index on clearAll', () => {
      const store = createRepoGitStore()
      store.upsert(repoKey('w1', '/a'), { workerId: 'w1', toplevel: '/a' })
      store.upsert(repoKey('w2', '/b'), { workerId: 'w2', toplevel: '/b' })
      store.clearAll()
      expect(store.keysForWorker('w1')).toEqual([])
      expect(store.keysForWorker('w2')).toEqual([])
    })

    it('recomputation tracks keysForWorker when the index changes', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        let reads = 0
        const count = createMemo(() => {
          reads++
          return store.keysForWorker('w1').length
        })

        expect(count()).toBe(0)
        const afterInit = reads

        store.upsert(repoKey('w1', '/repo'), { workerId: 'w1', toplevel: '/repo' })
        expect(count()).toBe(1)
        expect(reads).toBeGreaterThan(afterInit)

        const afterAdd = reads
        store.upsert(repoKey('w1', '/repo'), { branch: 'feature' })
        expect(count()).toBe(1)
        expect(reads).toBe(afterAdd)

        store.clear(repoKey('w1', '/repo'))
        expect(count()).toBe(0)
        expect(reads).toBeGreaterThan(afterAdd)

        dispose()
      })
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

    it('does not loop when upsert runs inside a reactive computation that seeds tabs', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('w1', '/repo')
        let runs = 0
        createMemo(() => {
          runs++
          // Same pattern as WorkspaceTabTree.interactions: seed while building
          // a value a parent render tracks. Without untrack in upsert, this
          // would read-then-write the same key and Solid would abort.
          store.upsert(key, { workerId: 'w1', toplevel: '/repo', branch: 'main' })
          return store.get(key)?.branch
        })
        expect(runs).toBe(1)
        expect(store.get(key)?.branch).toBe('main')
        dispose()
      })
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

    it('ignores a stale nested refresh when a newer toplevel refresh already applied', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const canonicalKey = repoKey('worker1', '/repo')
        let resolveNested!: (value: unknown) => void
        const nestedRpc = new Promise((resolve) => {
          resolveNested = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(nestedRpc)
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            status: { toplevel: '/repo', branch: 'from-toplevel' },
            files: [{ path: 'toplevel.txt' }],
          })

        const nested = store.refresh('worker1', '/repo/pkg', { repoKey: repoKey('worker1', '/repo/pkg') })
        await store.refresh('worker1', '/repo')
        resolveNested({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'from-nested-stale' },
          files: [{ path: 'nested.txt' }],
        })
        await nested

        expect(store.get(canonicalKey)?.branch).toBe('from-toplevel')
        expect(store.get(canonicalKey)?.files.map(f => f.path)).toEqual(['toplevel.txt'])

        dispose()
      })
    })

    it('realigns focusedKey to canonical when a stale nested refresh is blocked', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const probeKey = repoKey('worker1', '/repo/pkg')
        const canonicalKey = repoKey('worker1', '/repo')
        store.setFocusedKey(probeKey)

        let resolveNested!: (value: unknown) => void
        const nestedRpc = new Promise((resolve) => {
          resolveNested = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(nestedRpc)
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            status: { toplevel: '/repo', branch: 'from-toplevel' },
            files: [{ path: 'toplevel.txt' }],
          })

        const nested = store.refresh('worker1', '/repo/pkg', { repoKey: probeKey })
        await store.refresh('worker1', '/repo')
        resolveNested({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'from-nested-stale' },
          files: [{ path: 'nested.txt' }],
        })
        await nested

        expect(store.focusedKey()).toBe(canonicalKey)
        expect(store.get(probeKey)).toBeUndefined()
        expect(store.get(canonicalKey)?.branch).toBe('from-toplevel')

        dispose()
      })
    })

    it('clears a branch pin when a same-path refresh cancels this one without a later keeper', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const otherKey = repoKey('worker1', '/other')
        store.upsert(otherKey, {
          workerId: 'worker1',
          toplevel: '/other',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        let resolveFirst!: (value: unknown) => void
        const first = new Promise((resolve) => {
          resolveFirst = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(first)
          .mockRejectedValueOnce(new Error('worker unreachable'))

        const cancelled = store.refresh('worker1', '/other', { repoKey: otherKey })
        const newest = store.refresh('worker1', '/other', { repoKey: otherKey })
        await newest
        resolveFirst({
          repoRoot: '/other',
          status: { toplevel: '/other', branch: 'stale' },
          files: [],
        })
        await cancelled

        expect(store.get(otherKey)?.branch).toBe('feature')
        expect(store.get(otherKey)?.branchPinnedUntilRefresh).toBe(false)

        dispose()
      })
    })

    it('applies a refresh result even when another path refreshed meanwhile', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        let resolveOther!: (value: unknown) => void
        const otherRpc = new Promise((resolve) => {
          resolveOther = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(otherRpc)
          .mockResolvedValueOnce({
            repoRoot: '/active',
            status: { toplevel: '/active', branch: 'main' },
            files: [{ path: 'a.txt' }],
          })

        const other = store.refresh('worker1', '/other', { repoKey: repoKey('worker1', '/other') })
        await store.refresh('worker1', '/active')
        resolveOther({
          repoRoot: '/other',
          status: { toplevel: '/other', branch: 'dev', originUrl: 'o' },
          files: [{ path: 'b.txt' }],
        })
        await other

        expect(store.get(repoKey('worker1', '/active'))?.branch).toBe('main')
        expect(store.get(repoKey('worker1', '/active'))?.files.map(f => f.path)).toEqual(['a.txt'])
        expect(store.get(repoKey('worker1', '/other'))?.branch).toBe('dev')
        expect(store.get(repoKey('worker1', '/other'))?.files.map(f => f.path)).toEqual(['b.txt'])

        dispose()
      })
    })

    it('keeps a branch pin when a same-path refresh cancels this one', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        let resolveFirst!: (value: unknown) => void
        const first = new Promise((resolve) => {
          resolveFirst = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(first)
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            // RPC still reports the old name; pin must survive until this refresh applies.
            status: { toplevel: '/repo', branch: 'main' },
            files: [],
          })

        const cancelled = store.refresh('worker1', '/repo', { repoKey: key })
        const newest = store.refresh('worker1', '/repo', { repoKey: key })
        await newest
        // Stale RPC returns after the successor finished and kept the pin.
        resolveFirst({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'stale' },
          files: [],
        })
        await cancelled

        // Newest refresh saw pin + mismatched RPC branch and kept the stamp.
        expect(store.get(key)?.branch).toBe('feature')
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)

        dispose()
      })
    })

    it('keeps a same-path pin after a cancelled nested probe', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
          gitStatusSeen: true,
        })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'main' },
          files: [],
        })
        await store.refresh('worker1', '/repo', { repoKey: key })
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)

        let resolveNested!: (value: unknown) => void
        const nestedRpc = new Promise((resolve) => {
          resolveNested = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(nestedRpc)
          .mockRejectedValueOnce(new Error('superseded'))

        const nested = store.refresh('worker1', '/repo/pkg', { repoKey: repoKey('worker1', '/repo/pkg') })
        await store.refresh('worker1', '/repo/pkg', { repoKey: repoKey('worker1', '/repo/pkg') })
        resolveNested({
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        })
        await nested

        expect(store.get(key)?.branch).toBe('feature')
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)

        dispose()
      })
    })

    it('keeps the branch pin when ignoring a transient non-repo for a healthy repo', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
          gitStatusSeen: true,
          diffAdded: 3,
        })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        })

        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branch).toBe('feature')
        expect(store.get(key)?.diffAdded).toBe(3)
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)

        dispose()
      })
    })

    it('keeps a same-path pin after a later different-path refresh finishes', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const repoKeyPath = repoKey('worker1', '/repo')
        store.upsert(repoKeyPath, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        let resolveFirst!: (value: unknown) => void
        const first = new Promise((resolve) => {
          resolveFirst = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(first)
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            status: { toplevel: '/repo', branch: 'main' },
            files: [],
          })
          .mockResolvedValueOnce({
            repoRoot: '/active',
            status: { toplevel: '/active', branch: 'main' },
            files: [],
          })

        const cancelled = store.refresh('worker1', '/repo', { repoKey: repoKeyPath })
        const samePath = store.refresh('worker1', '/repo', { repoKey: repoKeyPath })
        await samePath
        expect(store.get(repoKeyPath)?.branchPinnedUntilRefresh).toBe(true)

        // A different-path refresh must not erase the same-path pin record.
        await store.refresh('worker1', '/active')
        resolveFirst({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'stale' },
          files: [],
        })
        await cancelled

        expect(store.get(repoKeyPath)?.branch).toBe('feature')
        expect(store.get(repoKeyPath)?.branchPinnedUntilRefresh).toBe(true)
        expect(store.get(repoKey('worker1', '/active'))?.branch).toBe('main')

        dispose()
      })
    })

    it('keeps a same-path pin while a later different-path refresh is in flight', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const repoKeyPath = repoKey('worker1', '/repo')
        store.upsert(repoKeyPath, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        let resolveFirst!: (value: unknown) => void
        let resolveActive!: (value: unknown) => void
        const first = new Promise((resolve) => {
          resolveFirst = resolve
        })
        const activeRpc = new Promise((resolve) => {
          resolveActive = resolve
        })
        mockGetGitFileStatus
          .mockReturnValueOnce(first)
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            status: { toplevel: '/repo', branch: 'main' },
            files: [],
          })
          .mockReturnValueOnce(activeRpc)

        const cancelled = store.refresh('worker1', '/repo', { repoKey: repoKeyPath })
        await store.refresh('worker1', '/repo', { repoKey: repoKeyPath })
        const active = store.refresh('worker1', '/active')
        resolveFirst({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'stale' },
          files: [],
        })
        await cancelled

        expect(store.get(repoKeyPath)?.branch).toBe('feature')
        expect(store.get(repoKeyPath)?.branchPinnedUntilRefresh).toBe(true)

        resolveActive({
          repoRoot: '/active',
          status: { toplevel: '/active', branch: 'main' },
          files: [],
        })
        await active

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

    /**
     * The allowance above covers ONE answer, because one answer can be
     * transient. Two in a row are the worker's real report.
     *
     * Without the limit the first non-repo answer suppresses every later one
     * for the life of the page. A user whose worktree is removed while the
     * link is down then keeps a branch label and a diff badge in the sidebar
     * for a directory that does not exist, and the Files section never
     * receives the "not a git repository" hint.
     */
    it('writes the non-repo stub on the second answer in a row', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, { workerId: 'worker1', toplevel: '/repo', branch: 'main', gitStatusSeen: true })

        const notARepo = {
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        }

        mockGetGitFileStatus.mockResolvedValueOnce(notARepo)
        await store.refresh('worker1', '/repo', { repoKey: key })
        expect(store.get(key)?.branch, 'one answer can be transient').toBe('main')

        mockGetGitFileStatus.mockResolvedValueOnce(notARepo)
        await store.refresh('worker1', '/repo', { repoKey: key })

        const state = store.get(key)!
        expect(state.toplevel, 'the second answer is believed').toBe('')
        expect(state.branch).toBe('')
        expect(state.errorHint).toBe('not a git repository')
        dispose()
      })
    })

    it('gives the allowance back after a real status lands between two non-repo answers', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, { workerId: 'worker1', toplevel: '/repo', branch: 'main', gitStatusSeen: true })

        const notARepo = {
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        }

        mockGetGitFileStatus.mockResolvedValueOnce(notARepo)
        await store.refresh('worker1', '/repo', { repoKey: key })

        // The repo answers, so the entry recovers and its allowance resets.
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'main' },
          files: [],
          errorHint: '',
        })
        await store.refresh('worker1', '/repo', { repoKey: key })
        expect(store.get(key)?.nonRepoProbeIgnored).toBe(false)

        // A later single answer is transient again, so the branch survives it.
        mockGetGitFileStatus.mockResolvedValueOnce(notARepo)
        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branch, 'the reset restores the allowance').toBe('main')
        expect(store.get(key)?.toplevel).toBe('/repo')
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

    it('clears the branch pin when writing a non-repo stub', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/plain-dir')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/plain-dir',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          status: undefined,
          files: [],
          errorHint: 'not a git repository',
        })

        await store.refresh('worker1', '/plain-dir', { repoKey: key })

        expect(store.get(key)?.toplevel).toBe('')
        expect(store.get(key)?.branch).toBe('')
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)
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

    it('does not record completion when refresh fails so a later failure can still clear the pin', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        mockGetGitFileStatus
          .mockRejectedValueOnce(new Error('first failure'))
          .mockRejectedValueOnce(new Error('second failure'))

        await store.refresh('worker1', '/repo', { repoKey: key })
        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)
        dispose()
      })
    })

    it('clears a restamped branch pin when refresh fails after an earlier refresh kept the pin', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'main' },
          files: [],
        })
        await store.refresh('worker1', '/repo', { repoKey: key })
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)

        store.upsert(key, { branch: 'other', branchPinnedUntilRefresh: true })

        mockGetGitFileStatus.mockRejectedValueOnce(new Error('branch refresh failed'))
        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branch).toBe('other')
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)
        dispose()
      })
    })

    it('clears a restamped branch pin when refresh fails after an earlier successful refresh', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'main',
        })

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          status: { toplevel: '/repo', branch: 'main' },
          files: [],
        })
        await store.refresh('worker1', '/repo', { repoKey: key })

        store.upsert(key, { branch: 'feature', branchPinnedUntilRefresh: true })

        mockGetGitFileStatus.mockRejectedValueOnce(new Error('branch refresh failed'))
        await store.refresh('worker1', '/repo', { repoKey: key })

        expect(store.get(key)?.branch).toBe('feature')
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)
        dispose()
      })
    })

    it('drops completed refresh gens when the branch pin clears', async () => {
      await createRoot(async (dispose) => {
        const store = createRepoGitStore()
        const key = repoKey('worker1', '/repo')
        store.upsert(key, {
          workerId: 'worker1',
          toplevel: '/repo',
          branch: 'feature',
          branchPinnedUntilRefresh: true,
        })

        mockGetGitFileStatus
          .mockResolvedValueOnce({
            repoRoot: '/repo',
            status: { toplevel: '/repo', branch: 'feature' },
            files: [],
          })
          .mockRejectedValueOnce(new Error('second failure'))

        await store.refresh('worker1', '/repo', { repoKey: key })
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)

        await store.refresh('worker1', '/repo', { repoKey: key })
        expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)

        dispose()
      })
    })
  })

  /**
   * The store exposes no per-worker clear, and must not grow one back.
   *
   * Dropping a worker's entries is never right while its tab rows survive. A
   * row keeps `gitToplevel`, `WorkspaceTabTree.repoKeyAndLabel` groups the tab
   * by that field, and the branch label comes from this store -- so a cleared
   * entry puts every tab of that worker under its repo with no branch name.
   * Neither event that used to call it removes the rows: a dropped link leaves
   * them, and so does deregistration.
   */
  it('exposes no per-worker clear', () => {
    expect(createRepoGitStore()).not.toHaveProperty('clearForWorker')
  })

  /**
   * The cap is the store's only reclaim path, so it has to bite -- and it has
   * to miss the entries a tab is reading, or it recreates the missing branch
   * label that the removal of the sweeps exists to prevent.
   */
  describe('least-recently-used cap', () => {
    const MAX = 256
    const fill = (store: ReturnType<typeof createRepoGitStore>, from: number, to: number) => {
      for (let i = from; i < to; i++)
        store.upsert(repoKey('w1', `/r${i}`), { workerId: 'w1', toplevel: `/r${i}` })
    }

    // `get` is the tab-facing read and it MARKS the entry as recently used, so
    // an assertion must not use it to look at an entry it is not pretending to
    // render. `repos()` is the raw map and touches nothing.
    const peek = (store: ReturnType<typeof createRepoGitStore>, key: string) => store.repos()[key as never]

    it('drops the coldest entry once the cap is passed', () => {
      const store = createRepoGitStore()
      fill(store, 0, MAX)
      expect(peek(store, repoKey('w1', '/r0')), 'still under the cap').toBeDefined()

      // Written last, so /r0 is now the least recently used.
      store.upsert(repoKey('w1', '/over'), { workerId: 'w1', toplevel: '/over' })

      expect(peek(store, repoKey('w1', '/r0'))).toBeUndefined()
      expect(peek(store, repoKey('w1', '/over'))).toBeDefined()
      expect(store.keysForWorker('w1'), 'the index shrinks with the map').toHaveLength(MAX)
    })

    it('keeps an entry a tab keeps reading, however old the write', () => {
      const store = createRepoGitStore()
      const live = repoKey('w1', '/r0')
      fill(store, 0, MAX)

      // What the sidebar does for a tab on every render.
      expect(store.get(live)).toBeDefined()
      store.upsert(repoKey('w1', '/over'), { workerId: 'w1', toplevel: '/over' })

      expect(peek(store, live), 'a read is what keeps a live tab\'s repo warm').toBeDefined()
      expect(peek(store, repoKey('w1', '/r1')), 'the next-coldest went instead').toBeUndefined()
    })

    it('keeps a pinned entry, which holds in-flight optimistic state', () => {
      const store = createRepoGitStore()
      const pinned = repoKey('w1', '/r0')
      store.upsert(pinned, {
        workerId: 'w1',
        toplevel: '/r0',
        branch: 'feature/x',
        branchPinnedUntilRefresh: true,
      })
      fill(store, 1, MAX + 1)

      expect(peek(store, pinned)?.branch).toBe('feature/x')
      expect(peek(store, repoKey('w1', '/r1')), 'the coldest unpinned entry went instead').toBeUndefined()
    })
  })
})
