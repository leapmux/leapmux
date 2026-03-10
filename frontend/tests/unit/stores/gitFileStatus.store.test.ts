import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'

// Mock workerRpc to control refresh() responses.
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
    ...overrides,
  }
}

describe('gitFileStatusStore', () => {
  describe('getDirDiffStats', () => {
    it('includes untracked file lines in diff stats', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
              linesAdded: 10,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const stats = store.getDirDiffStats('/repo')
        expect(stats.added).toBe(10)
        expect(stats.deleted).toBe(0)

        dispose()
      })
    })

    it('sums tracked and untracked file lines', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'modified.txt',
              unstagedStatus: GitFileStatusCode.MODIFIED,
              linesAdded: 5,
              linesDeleted: 2,
            }),
            makeEntry({
              path: 'staged.txt',
              stagedStatus: GitFileStatusCode.ADDED,
              stagedLinesAdded: 20,
            }),
            makeEntry({
              path: 'untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
              linesAdded: 8,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const stats = store.getDirDiffStats('/repo')
        expect(stats.added).toBe(5 + 20 + 8)
        expect(stats.deleted).toBe(2)

        dispose()
      })
    })

    it('scopes stats to subdirectory', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'src/untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
              linesAdded: 7,
            }),
            makeEntry({
              path: 'other.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
              linesAdded: 3,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const srcStats = store.getDirDiffStats('/repo/src')
        expect(srcStats.added).toBe(7)

        const rootStats = store.getDirDiffStats('/repo')
        expect(rootStats.added).toBe(10)

        dispose()
      })
    })
  })

  describe('getChangedFiles', () => {
    it('includes untracked files in changed and unstaged filters', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
              linesAdded: 5,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        expect(store.getChangedFiles('changed')).toHaveLength(1)
        expect(store.getChangedFiles('unstaged')).toHaveLength(1)
        expect(store.getChangedFiles('staged')).toHaveLength(0)

        dispose()
      })
    })
  })
})
